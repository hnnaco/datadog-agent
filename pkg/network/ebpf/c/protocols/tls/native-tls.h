#ifndef __NATIVE_TLS_H
#define __NATIVE_TLS_H

#include "ktypes.h"
#include "bpf_builtins.h"
#include "bpf_bypass.h"

#include "protocols/tls/native-tls-maps.h"

SEC("uprobe/SSL_do_handshake")
int BPF_BYPASSABLE_UPROBE(uprobe__SSL_do_handshake, void *ssl_ctx) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    log_debug("uprobe/SSL_do_handshake: pid_tgid=%llx ssl_ctx=%p", pid_tgid, ssl_ctx);
    bpf_map_update_with_telemetry(ssl_ctx_by_pid_tgid, &pid_tgid, &ssl_ctx, BPF_ANY);
    return 0;
}

SEC("uretprobe/SSL_do_handshake")
int BPF_BYPASSABLE_URETPROBE(uretprobe__SSL_do_handshake) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    log_debug("uretprobe/SSL_do_handshake: pid_tgid=%llx", pid_tgid);
    bpf_map_delete_elem(&ssl_ctx_by_pid_tgid, &pid_tgid);
    return 0;
}

SEC("uprobe/SSL_connect")
int BPF_BYPASSABLE_UPROBE(uprobe__SSL_connect, void *ssl_ctx) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    log_debug("uprobe/SSL_connect: pid_tgid=%llx ssl_ctx=%p", pid_tgid, ssl_ctx);
    bpf_map_update_with_telemetry(ssl_ctx_by_pid_tgid, &pid_tgid, &ssl_ctx, BPF_ANY);
    return 0;
}

SEC("uretprobe/SSL_connect")
int BPF_BYPASSABLE_URETPROBE(uretprobe__SSL_connect) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    log_debug("uretprobe/SSL_connect: pid_tgid=%llx", pid_tgid);
    bpf_map_delete_elem(&ssl_ctx_by_pid_tgid, &pid_tgid);
    return 0;
}

// this uprobe is essentially creating an index mapping a SSL context to a conn_tuple_t
SEC("uprobe/SSL_set_fd")
int BPF_BYPASSABLE_UPROBE(uprobe__SSL_set_fd, void *ssl_ctx, u32 socket_fd) {
    log_debug("uprobe/SSL_set_fd: ctx=%p fd=%d", ssl_ctx, socket_fd);
    init_ssl_sock(ssl_ctx, socket_fd);
    return 0;
}

SEC("uprobe/BIO_new_socket")
int BPF_BYPASSABLE_UPROBE(uprobe__BIO_new_socket, u32 socket_fd) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    log_debug("uprobe/BIO_new_socket: pid_tgid=%llx fd=%d", pid_tgid, socket_fd);
    bpf_map_update_with_telemetry(bio_new_socket_args, &pid_tgid, &socket_fd, BPF_ANY);
    return 0;
}

SEC("uretprobe/BIO_new_socket")
int BPF_BYPASSABLE_URETPROBE(uretprobe__BIO_new_socket, void *bio) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    log_debug("uretprobe/BIO_new_socket: pid_tgid=%llx", pid_tgid);
    u32 *socket_fd = bpf_map_lookup_elem(&bio_new_socket_args, &pid_tgid);
    if (socket_fd == NULL) {
        return 0;
    }

    if (bio == NULL) {
        goto cleanup;
    }
    u32 fd = *socket_fd; // copy map value into stack (required by older Kernels)
    bpf_map_update_with_telemetry(fd_by_ssl_bio, &bio, &fd, BPF_ANY);
cleanup:
    bpf_map_delete_elem(&bio_new_socket_args, &pid_tgid);
    return 0;
}

SEC("uprobe/SSL_set_bio")
int BPF_BYPASSABLE_UPROBE(uprobe__SSL_set_bio, void *ssl_ctx, void *bio) {
    log_debug("uprobe/SSL_set_bio: ctx=%p bio=%p", ssl_ctx, bio);
    u32 *socket_fd = bpf_map_lookup_elem(&fd_by_ssl_bio, &bio);
    if (socket_fd == NULL) {
        return 0;
    }
    init_ssl_sock(ssl_ctx, *socket_fd);
    bpf_map_delete_elem(&fd_by_ssl_bio, &bio);
    return 0;
}

SEC("uprobe/SSL_read")
int BPF_BYPASSABLE_UPROBE(uprobe__SSL_read) {
    ssl_read_args_t args = { 0 };
    args.ctx = (void *)PT_REGS_PARM1(ctx);
    args.buf = (void *)PT_REGS_PARM2(ctx);
    u64 pid_tgid = bpf_get_current_pid_tgid();
    log_debug("uprobe/SSL_read: pid_tgid=%llx ctx=%p", pid_tgid, args.ctx);
    bpf_map_update_with_telemetry(ssl_read_args, &pid_tgid, &args, BPF_ANY);

    // Trigger mapping of SSL context to connection tuple in case it is missing.
    tup_from_ssl_ctx(args.ctx, pid_tgid);
    return 0;
}

static __always_inline int SSL_read_ret(struct pt_regs *ctx, __u64 tags) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    int len = (int)PT_REGS_RC(ctx);
    if (len <= 0) {
        log_debug("uretprobe/SSL_read: pid_tgid=%llx ret=%d", pid_tgid, len);
        goto cleanup;
    }

    log_debug("uretprobe/SSL_read: pid_tgid=%llx", pid_tgid);
    ssl_read_args_t *args = bpf_map_lookup_elem(&ssl_read_args, &pid_tgid);
    if (args == NULL) {
        return 0;
    }

    void *ssl_ctx = args->ctx;
    conn_tuple_t *t = tup_from_ssl_ctx(ssl_ctx, pid_tgid);
    if (t == NULL) {
        log_debug("uretprobe/SSL_read: pid_tgid=%llx ctx=%p: no conn tuple", pid_tgid, ssl_ctx);
        goto cleanup;
    }

    char *buffer_ptr = args->buf;
    bpf_map_delete_elem(&ssl_read_args, &pid_tgid);
    // The read tuple should be flipped (compared to the write tuple).
    // tls_process and the appropriate parsers will flip it back if needed.
    conn_tuple_t copy = {0};
    bpf_memcpy(&copy, t, sizeof(conn_tuple_t));
    // We want to guarantee write-TLS hooks generates the same connection tuple, while read-TLS hooks generate
    // the inverse direction, thus we're normalizing the tuples into a client <-> server direction.
    normalize_tuple(&copy);
    tls_process(ctx, &copy, buffer_ptr, len, tags);
    return 0;
cleanup:
    bpf_map_delete_elem(&ssl_read_args, &pid_tgid);
    return 0;
}

SEC("uretprobe/SSL_read")
int BPF_BYPASSABLE_URETPROBE(uretprobe__SSL_read) {
    return SSL_read_ret(ctx, LIBSSL);
}

SEC("uretprobe/SSL_read")
int BPF_BYPASSABLE_URETPROBE(istio_uretprobe__SSL_read) {
    return SSL_read_ret(ctx, ISTIO);
}

SEC("uretprobe/SSL_read")
int BPF_BYPASSABLE_URETPROBE(nodejs_uretprobe__SSL_read) {
    return SSL_read_ret(ctx, NODEJS);
}

SEC("uprobe/SSL_write")
int BPF_BYPASSABLE_UPROBE(uprobe__SSL_write) {
    ssl_write_args_t args = {0};
    args.ctx = (void *)PT_REGS_PARM1(ctx);
    args.buf = (void *)PT_REGS_PARM2(ctx);
    u64 pid_tgid = bpf_get_current_pid_tgid();
    log_debug("uprobe/SSL_write: pid_tgid=%llx ctx=%p", pid_tgid, args.ctx);
    bpf_map_update_with_telemetry(ssl_write_args, &pid_tgid, &args, BPF_ANY);
    return 0;
}

static __always_inline int SSL_write_ret(struct pt_regs* ctx, __u64 flags) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    int write_len = (int)PT_REGS_RC(ctx);
    log_debug("uretprobe/SSL_write: pid_tgid=%llx len=%d", pid_tgid, write_len);
    if (write_len <= 0) {
        goto cleanup;
    }

    ssl_write_args_t *args = bpf_map_lookup_elem(&ssl_write_args, &pid_tgid);
    if (args == NULL) {
        return 0;
    }

    conn_tuple_t *t = tup_from_ssl_ctx(args->ctx, pid_tgid);
    if (t == NULL) {
        goto cleanup;
    }

    char *buffer_ptr = args->buf;
    bpf_map_delete_elem(&ssl_write_args, &pid_tgid);
    conn_tuple_t copy = {0};
    bpf_memcpy(&copy, t, sizeof(conn_tuple_t));
    // We want to guarantee write-TLS hooks generates the same connection tuple, while read-TLS hooks generate
    // the inverse direction, thus we're normalizing the tuples into a client <-> server direction, and then flipping it
    // to the server <-> client direction.
    normalize_tuple(&copy);
    flip_tuple(&copy);
    tls_process(ctx, &copy, buffer_ptr, write_len, flags);
    return 0;
cleanup:
    bpf_map_delete_elem(&ssl_write_args, &pid_tgid);
    return 0;
}

SEC("uretprobe/SSL_write")
int BPF_BYPASSABLE_URETPROBE(uretprobe__SSL_write) {
    return SSL_write_ret(ctx, LIBSSL);
}

SEC("uretprobe/SSL_write")
int BPF_BYPASSABLE_URETPROBE(istio_uretprobe__SSL_write) {
    return SSL_write_ret(ctx, ISTIO);
}

SEC("uretprobe/SSL_write")
int BPF_BYPASSABLE_URETPROBE(nodejs_uretprobe__SSL_write) {
    return SSL_write_ret(ctx, NODEJS);
}

SEC("uprobe/SSL_read_ex")
int BPF_BYPASSABLE_UPROBE(uprobe__SSL_read_ex) {
    ssl_read_ex_args_t args = {0};
    args.ctx = (void *)PT_REGS_PARM1(ctx);
    args.buf = (void *)PT_REGS_PARM2(ctx);
    args.size_out_param = (size_t *)PT_REGS_PARM4(ctx);
    u64 pid_tgid = bpf_get_current_pid_tgid();
    log_debug("uprobe/SSL_read_ex: pid_tgid=%llx ctx=%p", pid_tgid, args.ctx);
    bpf_map_update_with_telemetry(ssl_read_ex_args, &pid_tgid, &args, BPF_ANY);

    // Trigger mapping of SSL context to connection tuple in case it is missing.
    tup_from_ssl_ctx(args.ctx, pid_tgid);
    return 0;
}

static __always_inline int SSL_read_ex_ret(struct pt_regs* ctx, __u64 tags) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    const int return_code = (int)PT_REGS_RC(ctx);
    if (return_code != 1) {
        log_debug("uretprobe/SSL_read_ex: failed pid_tgid=%llx ret=%d", pid_tgid, return_code);
        goto cleanup;
    }

    ssl_read_ex_args_t *args = bpf_map_lookup_elem(&ssl_read_ex_args, &pid_tgid);
    if (args == NULL) {
        log_debug("uretprobe/SSL_read_ex: no args pid_tgid=%llx", pid_tgid);
        return 0;
    }

    if (args->size_out_param == NULL) {
        log_debug("uretprobe/SSL_read_ex: pid_tgid=%llx buffer size out param is null", pid_tgid);
        goto cleanup;
    }

    size_t bytes_count = 0;
    bpf_probe_read_user(&bytes_count, sizeof(bytes_count), args->size_out_param);
    if ( bytes_count <= 0) {
        log_debug("uretprobe/SSL_read_ex: read non positive number of bytes (pid_tgid=%llx len=%zu)", pid_tgid, bytes_count);
        goto cleanup;
    }

    void *ssl_ctx = args->ctx;
    conn_tuple_t *conn_tuple = tup_from_ssl_ctx(ssl_ctx, pid_tgid);
    if (conn_tuple == NULL) {
        log_debug("uretprobe/SSL_read_ex: pid_tgid=%llx ctx=%p: no conn tuple", pid_tgid, ssl_ctx);
        goto cleanup;
    }

    char *buffer_ptr = args->buf;
    bpf_map_delete_elem(&ssl_read_ex_args, &pid_tgid);
    // The read tuple should be flipped (compared to the write tuple).
    // tls_process and the appropriate parsers will flip it back if needed.
    conn_tuple_t copy = {0};
    bpf_memcpy(&copy, conn_tuple, sizeof(conn_tuple_t));
    // We want to guarantee write-TLS hooks generates the same connection tuple, while read-TLS hooks generate
    // the inverse direction, thus we're normalizing the tuples into a client <-> server direction.
    normalize_tuple(&copy);
    tls_process(ctx, &copy, buffer_ptr, bytes_count, tags);
    return 0;
cleanup:
    bpf_map_delete_elem(&ssl_read_ex_args, &pid_tgid);
    return 0;
}

SEC("uretprobe/SSL_read_ex")
int BPF_BYPASSABLE_URETPROBE(uretprobe__SSL_read_ex) {
    return SSL_read_ex_ret(ctx, LIBSSL);
}

SEC("uretprobe/SSL_read_ex")
int BPF_BYPASSABLE_URETPROBE(nodejs_uretprobe__SSL_read_ex) {
    return SSL_read_ex_ret(ctx, NODEJS);
}

SEC("uprobe/SSL_write_ex")
int BPF_BYPASSABLE_UPROBE(uprobe__SSL_write_ex) {
    ssl_write_ex_args_t args = {0};
    args.ctx = (void *)PT_REGS_PARM1(ctx);
    args.buf = (void *)PT_REGS_PARM2(ctx);
    args.size_out_param = (size_t *)PT_REGS_PARM4(ctx);
    u64 pid_tgid = bpf_get_current_pid_tgid();
    log_debug("uprobe/SSL_write_ex: pid_tgid=%llx ctx=%p", pid_tgid, args.ctx);
    bpf_map_update_with_telemetry(ssl_write_ex_args, &pid_tgid, &args, BPF_ANY);
    return 0;
}

static __always_inline int SSL_write_ex_ret(struct pt_regs* ctx, __u64 tags) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    const int return_code = (int)PT_REGS_RC(ctx);
    if (return_code != 1) {
        log_debug("uretprobe/SSL_write_ex: failed pid_tgid=%llx len=%d", pid_tgid, return_code);
        goto cleanup;
    }

    ssl_write_ex_args_t *args = bpf_map_lookup_elem(&ssl_write_ex_args, &pid_tgid);
    if (args == NULL) {
        log_debug("uretprobe/SSL_write_ex: no args pid_tgid=%llx", pid_tgid);
        return 0;
    }

    if (args->size_out_param == NULL) {
        log_debug("uretprobe/SSL_write_ex: pid_tgid=%llx buffer size out param is null", pid_tgid);
        goto cleanup;
    }

    size_t bytes_count = 0;
    bpf_probe_read_user(&bytes_count, sizeof(bytes_count), args->size_out_param);
    if ( bytes_count <= 0) {
        log_debug("uretprobe/SSL_write_ex: wrote non positive number of bytes (pid_tgid=%llx len=%zu)", pid_tgid, bytes_count);
        goto cleanup;
    }

    conn_tuple_t *conn_tuple = tup_from_ssl_ctx(args->ctx, pid_tgid);
    if (conn_tuple == NULL) {
        log_debug("uretprobe/SSL_write_ex: pid_tgid=%llx: no conn tuple", pid_tgid);
        goto cleanup;
    }

    char *buffer_ptr = args->buf;
    bpf_map_delete_elem(&ssl_write_ex_args, &pid_tgid);
    conn_tuple_t copy = {0};
    bpf_memcpy(&copy, conn_tuple, sizeof(conn_tuple_t));
    // We want to guarantee write-TLS hooks generates the same connection tuple, while read-TLS hooks generate
    // the inverse direction, thus we're normalizing the tuples into a client <-> server direction, and then flipping it
    // to the server <-> client direction.
    normalize_tuple(&copy);
    flip_tuple(&copy);
    tls_process(ctx, &copy, buffer_ptr, bytes_count, tags);
    return 0;
cleanup:
    bpf_map_delete_elem(&ssl_write_ex_args, &pid_tgid);
    return 0;
}

SEC("uretprobe/SSL_write_ex")
int BPF_BYPASSABLE_URETPROBE(uretprobe__SSL_write_ex) {
    return SSL_write_ex_ret(ctx, LIBSSL);
}

SEC("uretprobe/SSL_write_ex")
int BPF_BYPASSABLE_URETPROBE(nodejs_uretprobe__SSL_write_ex) {
    return SSL_write_ex_ret(ctx, NODEJS);
}

SEC("uprobe/SSL_shutdown")
int BPF_BYPASSABLE_UPROBE(uprobe__SSL_shutdown, void *ssl_ctx) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    log_debug("uprobe/SSL_shutdown: pid_tgid=%llx ctx=%p", pid_tgid, ssl_ctx);
    conn_tuple_t *t = tup_from_ssl_ctx(ssl_ctx, pid_tgid);
    if (t == NULL) {
        return 0;
    }

    // tls_finish can launch a tail call, thus cleanup should be done before.
    bpf_map_delete_elem(&ssl_sock_by_ctx, &ssl_ctx);
    tls_finish(ctx, t, false);

    return 0;
}

SEC("uprobe/gnutls_handshake")
int BPF_BYPASSABLE_UPROBE(uprobe__gnutls_handshake, void *ssl_ctx) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    bpf_map_update_with_telemetry(ssl_ctx_by_pid_tgid, &pid_tgid, &ssl_ctx, BPF_ANY);
    return 0;
}

SEC("uretprobe/gnutls_handshake")
int BPF_BYPASSABLE_URETPROBE(uretprobe__gnutls_handshake) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    bpf_map_delete_elem(&ssl_ctx_by_pid_tgid, &pid_tgid);
    return 0;
}

// void gnutls_transport_set_int (gnutls_session_t session, int fd)
// Note: this function is implemented as a macro in gnutls
// that calls gnutls_transport_set_int2, so no uprobe is needed

// void gnutls_transport_set_int2 (gnutls_session_t session, int recv_fd, int send_fd)
SEC("uprobe/gnutls_transport_set_int2")
int BPF_BYPASSABLE_UPROBE(uprobe__gnutls_transport_set_int2, void *ssl_session, int recv_fd) {
    // Use the recv_fd and ignore the send_fd;
    // in most real-world scenarios, they are the same.
    log_debug("gnutls_transport_set_int2: ctx=%p fd=%d", ssl_session, recv_fd);

    init_ssl_sock(ssl_session, (u32)recv_fd);
    return 0;
}

// void gnutls_transport_set_ptr (gnutls_session_t session, gnutls_transport_ptr_t ptr)
// "In berkeley style sockets this function will set the connection descriptor."
SEC("uprobe/gnutls_transport_set_ptr")
int BPF_BYPASSABLE_UPROBE(uprobe__gnutls_transport_set_ptr, void *ssl_session, int fd) {
    // This is a void*, but it might contain the socket fd cast as a pointer.
    log_debug("gnutls_transport_set_ptr: ctx=%p fd=%d", ssl_session, fd);

    init_ssl_sock(ssl_session, (u32)fd);
    return 0;
}

// void gnutls_transport_set_ptr2 (gnutls_session_t session, gnutls_transport_ptr_t recv_ptr, gnutls_transport_ptr_t send_ptr)
// "In berkeley style sockets this function will set the connection descriptor."
SEC("uprobe/gnutls_transport_set_ptr2")
int BPF_BYPASSABLE_UPROBE(uprobe__gnutls_transport_set_ptr2, void *ssl_session, int recv_fd) {
    // Use the recv_ptr and ignore the send_ptr;
    // in most real-world scenarios, they are the same.
    // This is a void*, but it might contain the socket fd cast as a pointer.
    log_debug("gnutls_transport_set_ptr2: ctx=%p fd=%d", ssl_session, recv_fd);

    init_ssl_sock(ssl_session, (u32)recv_fd);
    return 0;
}

// ssize_t gnutls_record_recv (gnutls_session_t session, void * data, size_t data_size)
SEC("uprobe/gnutls_record_recv")
int BPF_BYPASSABLE_UPROBE(uprobe__gnutls_record_recv, void *ssl_session, void *data) {
    // Re-use the map for SSL_read
    ssl_read_args_t args = {
        .ctx = ssl_session,
        .buf = data,
    };
    u64 pid_tgid = bpf_get_current_pid_tgid();
    log_debug("gnutls_record_recv: pid=%llu ctx=%p", pid_tgid, ssl_session);
    bpf_map_update_with_telemetry(ssl_read_args, &pid_tgid, &args, BPF_ANY);
    return 0;
}

// ssize_t gnutls_record_recv (gnutls_session_t session, void * data, size_t data_size)
SEC("uretprobe/gnutls_record_recv")
int BPF_BYPASSABLE_URETPROBE(uretprobe__gnutls_record_recv, ssize_t read_len) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    if (read_len <= 0) {
        goto cleanup;
    }

    // Re-use the map for SSL_read
    ssl_read_args_t *args = bpf_map_lookup_elem(&ssl_read_args, &pid_tgid);
    if (args == NULL) {
        return 0;
    }

    void *ssl_session = args->ctx;
    log_debug("uret/gnutls_record_recv: pid=%llu ctx=%p", pid_tgid, ssl_session);
    conn_tuple_t *t = tup_from_ssl_ctx(ssl_session, pid_tgid);
    if (t == NULL) {
        goto cleanup;
    }

    char *buffer_ptr = args->buf;
    bpf_map_delete_elem(&ssl_read_args, &pid_tgid);
    // The read tuple should be flipped (compared to the write tuple).
    // tls_process and the appropriate parsers will flip it back if needed.
    conn_tuple_t copy = {0};
    bpf_memcpy(&copy, t, sizeof(conn_tuple_t));
    // We want to guarantee write-TLS hooks generates the same connection tuple, while read-TLS hooks generate
    // the inverse direction, thus we're normalizing the tuples into a client <-> server direction.
    normalize_tuple(&copy);
    tls_process(ctx, &copy, buffer_ptr, read_len, LIBGNUTLS);
    return 0;
cleanup:
    bpf_map_delete_elem(&ssl_read_args, &pid_tgid);
    return 0;
}

// ssize_t gnutls_record_send (gnutls_session_t session, const void * data, size_t data_size)
SEC("uprobe/gnutls_record_send")
int BPF_BYPASSABLE_UPROBE(uprobe__gnutls_record_send) {
    ssl_write_args_t args = {0};
    args.ctx = (void *)PT_REGS_PARM1(ctx);
    args.buf = (void *)PT_REGS_PARM2(ctx);
    u64 pid_tgid = bpf_get_current_pid_tgid();
    log_debug("uprobe/gnutls_record_send: pid=%llu ctx=%p", pid_tgid, args.ctx);
    bpf_map_update_with_telemetry(ssl_write_args, &pid_tgid, &args, BPF_ANY);
    return 0;
}

SEC("uretprobe/gnutls_record_send")
int BPF_BYPASSABLE_URETPROBE(uretprobe__gnutls_record_send, ssize_t write_len) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    log_debug("uretprobe/gnutls_record_send: pid=%llu len=%zd", pid_tgid, write_len);
    if (write_len <= 0) {
        goto cleanup;
    }

    ssl_write_args_t *args = bpf_map_lookup_elem(&ssl_write_args, &pid_tgid);
    if (args == NULL) {
        return 0;
    }

    conn_tuple_t *t = tup_from_ssl_ctx(args->ctx, pid_tgid);
    if (t == NULL) {
        goto cleanup;
    }

    char *buffer_ptr = args->buf;
    bpf_map_delete_elem(&ssl_write_args, &pid_tgid);
    conn_tuple_t copy = {0};
    bpf_memcpy(&copy, t, sizeof(conn_tuple_t));
    // We want to guarantee write-TLS hooks generates the same connection tuple, while read-TLS hooks generate
    // the inverse direction, thus we're normalizing the tuples into a client <-> server direction, and then flipping it
    // to the server <-> client direction.
    normalize_tuple(&copy);
    flip_tuple(&copy);
    tls_process(ctx, &copy, buffer_ptr, write_len, LIBGNUTLS);
    return 0;
cleanup:
    bpf_map_delete_elem(&ssl_write_args, &pid_tgid);
    return 0;
}

static __always_inline void gnutls_goodbye(struct pt_regs *ctx, void *ssl_session) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    log_debug("gnutls_goodbye: pid=%llu ctx=%p", pid_tgid, ssl_session);
    conn_tuple_t *t = tup_from_ssl_ctx(ssl_session, pid_tgid);
    if (t == NULL) {
        return;
    }

    // tls_finish can launch a tail call, thus cleanup should be done before.
    bpf_map_delete_elem(&ssl_sock_by_ctx, &ssl_session);
    tls_finish(ctx, t, false);
}

// int gnutls_bye (gnutls_session_t session, gnutls_close_request_t how)
SEC("uprobe/gnutls_bye")
int BPF_BYPASSABLE_UPROBE(uprobe__gnutls_bye, void *ssl_session) {
    gnutls_goodbye(ctx, ssl_session);
    return 0;
}

// void gnutls_deinit (gnutls_session_t session)
SEC("uprobe/gnutls_deinit")
int BPF_BYPASSABLE_UPROBE(uprobe__gnutls_deinit, void *ssl_session) {
    gnutls_goodbye(ctx, ssl_session);
    return 0;
}

SEC("kprobe/tcp_sendmsg")
int BPF_BYPASSABLE_KPROBE(kprobe__tcp_sendmsg, struct sock *sk) {
    log_debug("kprobe/tcp_sendmsg: sk=%p", sk);
    // map connection tuple during SSL_do_handshake(ctx)
    map_ssl_ctx_to_sock(sk);
    return 0;
}

static __always_inline void delete_pid_in_maps() {
    u64 pid_tgid = bpf_get_current_pid_tgid();

    bpf_map_delete_elem(&ssl_read_args, &pid_tgid);
    bpf_map_delete_elem(&ssl_read_ex_args, &pid_tgid);
    bpf_map_delete_elem(&ssl_write_args, &pid_tgid);
    bpf_map_delete_elem(&ssl_write_ex_args, &pid_tgid);
    bpf_map_delete_elem(&ssl_ctx_by_pid_tgid, &pid_tgid);
    bpf_map_delete_elem(&bio_new_socket_args, &pid_tgid);
}

SEC("tracepoint/sched/sched_process_exit")
int tracepoint__sched__sched_process_exit(void *ctx) {
    CHECK_BPF_PROGRAM_BYPASSED()
    delete_pid_in_maps();
    return 0;
}

SEC("raw_tracepoint/sched_process_exit")
int raw_tracepoint__sched_process_exit(void *ctx) {
    CHECK_BPF_PROGRAM_BYPASSED()
    delete_pid_in_maps();
    return 0;
}

#endif
