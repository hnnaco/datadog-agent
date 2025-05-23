# Build the Agent packages

Agent packages for all the supported platforms are built using
[Omnibus](https://github.com/chef/omnibus), which can be run through [deva](https://datadoghq.dev/datadog-agent/setup/#tooling) commands.

Omnibus creates a package for your operating system, so you'll get a DEB
package on Debian-based distros, an RPM package on distros that use RPM, an MSI
installer on Windows, or a `.pkg` package bundled in a DMG archive on Mac.

For Linux, we provide Docker images (one to build DEB packages and one for RPM),
with the build dependencies installed, so you don't have to install them on your system.

## Linux Docker image (Linux host only, recommended)

Use the provided Docker images to build a DEB or RPM
package for Linux. You need to have Docker already running on your machine.

From the `datadog-agent` source folder, use the following command to run the
`omnibus.build` task in a Docker container:

```
docker run -v "$PWD:/go/src/github.com/DataDog/datadog-agent" -v "/tmp/omnibus:/omnibus" -v "/tmp/opt/datadog-agent:/opt/datadog-agent" -v"/tmp/gems:/gems" --workdir=/go/src/github.com/DataDog/datadog-agent datadog/agent-buildimages-linux-glibc-2-17-x64 dda inv -- -e omnibus.build --base-dir=/omnibus --gem-path=/gems
```

For `arm64`, use this image instead: `datadog/agent-buildimages-linux-glibc-2-23-arm64`

The container will share 3 volumes with the host to avoid starting from scratch
at each Omnibus run:

 * `/tmp/omnibus`, containing the Omnibus base dir
 * `/tmp/opt/datadog-agent`, containing the Omnibus installation dir
 * `/tmp/gems`, containing all the ruby gems installed with Bundler

If you want to find the Dockerfiles for these images, they are available in the
[datadog-agent-buildimages](https://github.com/DataDog/datadog-agent-buildimages) git repo.
To build them from scratch, you can do so like this:

```
docker build -t datadog-agent-buildimages:deb_x64 -f deb-x64/Dockerfile .
```

If the build images crash when you run them on modern Linux distributions, you might be
affected by [this bug](https://github.com/moby/moby/issues/28705).

## Building on your system (Linux and Mac)

The project will be built locally and provide a .tar.xz tarball (in the omnibus/pkg folder)
with the resulting artifacts by default on linux.
This artifact is the expected source when building a container image.
You can chose to generate an installable package in the form of a deb/rpm artifact by
providing a `OMNIBUS_FORCE_PACKAGES` environment variable during the build.
On macOS, a dmg artifact will always be generated.
Most of the files will be copied or created under the same installation path of
the final package, `/opt/datadog-agent`, but if you run Omnibus from Linux, some
files will be copied into `/etc`. This means two things:

 * If you already have a Datadog Agent installed, you might need to move it to a
   different location before operating Omnibus.
 * You need root privileges to build the packages (Linux only).

For these reasons, if you're running Linux we strongly suggest to use a dedicated
virtual machine or a Docker container where Omnibus can safely move things around
the filesystem without disrupting anything.

To run Omnibus and build the package, make the `/opt` folder world readable and run:

```
dda inv -- omnibus.build --base-dir=$HOME/.omnibus
```

On Mac, you might want to skip the signing step by running:

```
dda inv -- omnibus.build --base-dir=$HOME/.omnibus --skip-sign
```

The path you pass with the `--base-dir` option will contain the sources
downloaded by Omnibus in the `src` folder, the binaries cached after building
those sources in the `cache` folder and the final deb/rpm/dmg artifacts in the
`pkg` folder. You can fine tune an Omnibus run passing more options, see
`dda inv -- omnibus.build --help` for the list of all the available options.

**Note:** it's strongly advised to pass `--base-dir` and point to a directory
outside the Agent repo. By default Omnibus stores packages in the project folder
itself: running the task multiple times would recursively add those artifacts to
the source files for the `datadog-agent` software definition.

## Windows Docker image (Windows host only, recommended)

### Prerequisites
To build on Windows, [Docker Desktop](https://docs.docker.com/docker-for-windows/install/) must be installed and configured to use Windows containers.

Start a Powershell prompt and navigate to your local clone of the `datadog-agent` repo.

 Run the following command:

```powershell
docker run -v "$(Get-Location):c:\mnt" -e OMNIBUS_TARGET=main -e MAJOR_VERSION=7 -e TARGET_ARCH=x64 datadog/agent-buildimages-windows_x64:1809 powershell -C "c:\mnt\tasks\winbuildscripts\Build-AgentPackages.ps1 -BuildOutOfSource 1 -InstallDeps 1 -CheckGoVersion 1"
```

Downloading the Docker image may take some time in the first run.

Alternatively here's a small Powershell script to facilitate using the docker image:
```powershell
param (
   [int]$MAJOR_VERSION=7,
   $TARGET_ARCH="x64",
   [bool]$RM_CONTAINER=$true,
   [bool]$DEBUG=$false
)

$cmd = "docker run"
if ($RM_CONTAINER) {
    $cmd += " --rm "
}
$opts = "-e OMNIBUS_TARGET=main -e MAJOR_VERSION=$MAJOR_VERSION -e TARGET_ARCH=$TARGET_ARCH"
if ($DEBUG) {
    $opts += " -e DEBUG_CUSTOMACTION=yes "
}
$cmd += " -m 8192M -v ""$(Get-Location):c:\mnt"" $opts datadog/agent-buildimages-windows_x64:1809 powershell -C ""c:\mnt\tasks\winbuildscripts\Build-AgentPackages.ps1 -BuildOutOfSource 1 -InstallDeps 1 -CheckGoVersion 1"""
Write-Host $cmd
Invoke-Expression -Command $cmd
```

If the build succeeds, the build artifacts can be found under `omnibus\pkg` in the repo.
