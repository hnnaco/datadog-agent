"""
Agent release metrics collection scripts
"""

from datetime import datetime

from tasks.libs.ciproviders.github_api import GithubAPI
from tasks.libs.common.constants import GITHUB_REPO_NAME


def get_release_lead_time(cutoff_date, release_date):
    release_date = datetime.strptime(release_date, "%Y-%m-%d")
    cutoff_date = datetime.strptime(cutoff_date, "%Y-%m-%d")

    return (release_date - cutoff_date).days


def get_prs_metrics(milestone, cutoff_date):
    github = GithubAPI(repository=GITHUB_REPO_NAME)
    cutoff_date = datetime.strptime(cutoff_date, "%Y-%m-%d").date()
    pr_counts = {"total": 0, "before_cutoff": 0, "on_cutoff": 0, "after_cutoff": 0}
    m = get_milestone(github.repo, milestone)
    issues = github.repo.get_issues(m, state='closed')
    for issue in issues:
        if issue.pull_request is None or issue.pull_request.raw_data['merged_at'] is None:
            continue
        # until 3.11 we need to strip the date string
        merged = datetime.fromisoformat(issue.pull_request.raw_data['merged_at'][:-1]).date()
        if merged < cutoff_date:
            pr_counts["before_cutoff"] += 1
        elif merged == cutoff_date:
            pr_counts["on_cutoff"] += 1
        else:
            pr_counts["after_cutoff"] += 1
    pr_counts["total"] = pr_counts["before_cutoff"] + pr_counts["on_cutoff"] + pr_counts["after_cutoff"]
    return pr_counts


def get_milestone(repo, milestone):
    milestones = repo.get_milestones(state="all")
    for mile in milestones:
        if mile.title == milestone:
            return mile
    return None
