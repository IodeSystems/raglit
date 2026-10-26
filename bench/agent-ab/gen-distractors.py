#!/usr/bin/env python3
"""Write filler documents next to the fixture corpus: status notes, meeting
notes and retros that share the core documents' vocabulary but answer none of
the questions.

Why: on the 30 core documents alone, reading everything costs an agent a few
thousand tokens and grep has nothing to wade through. A real corpus is mostly
documents that mention the words in a question without answering it. This
adds that, deterministically (fixed seed), so two runs see the same corpus.

Usage: gen-distractors.py OUT_DIR [-n COUNT]
"""
import argparse, random
from pathlib import Path

TEAMS = ["infra", "billing", "ledger", "sensor firmware", "bootloader", "web", "mobile"]
PEOPLE = ["Calder Byrne", "Wren Halvorsen", "Tobias Ekwueme", "Odile Fenn",
          "Anselm Quade", "Bettina Corr", "Lio Marchetti", "Juno Akerele",
          "Corvin Pell", "Marisol Teague", "Oren Blythe", "Saskia Doyle"]

# Sentences that use a question's words without answering it.
NOISE = [
    "{p} is out on leave next week; {q} covers reviews.",
    "Planning for the Denver offsite continues; the venue shortlist is in the shared drive.",
    "Redis memory alerts on the staging box were noise from a load test.",
    "The marketing site certificate was renewed by the agency; nothing for us to do.",
    "We discussed the remote stipend survey results but made no decision.",
    "Rate limit errors in the partner sandbox were caused by a test script.",
    "Expense reports for the conference are due by the end of the month.",
    "{p} asked whether hotel bookings for the customer visit need pre-approval; Finance will answer.",
    "The on-call handoff doc needs an owner; {p} volunteered to draft it.",
    "Parental leave questions should go to People Operations, not managers.",
    "A deploy to prod-east was paused for unrelated latency in the CDN.",
    "The ledger reconciliation job ran long because of month-end volume.",
    "Checkout conversion dipped on Tuesday; analytics thinks it is seasonal.",
    "Vault was mentioned again as an option for secrets; no action.",
    "The 401(k) provider is changing its website; credentials carry over.",
    "{p} will look at the flaky integration test in billing-svc.",
    "We agreed to revisit the retry budget after the next incident review.",
    "Laptop refresh requests are backed up at IT; expect two weeks.",
    "The design review for the bootloader update slipped to next sprint.",
    "Postgres minor version upgrades are scheduled for the maintenance window.",
    "{p} filed travel expenses for the offsite late again; Finance sent a reminder.",
    "Travel expense questions keep coming up in standup; someone should write a FAQ.",
    "{p} asked how to expense the team dinner; nobody remembered the process.",
    "{p}'s laptop was slow after the update; IT reimaged it.",
    "The new laptops for the mobile team arrive next week.",
    "We need access to the analytics warehouse for the new dashboards; {p} will ask.",
    "{p} still has no access to the staging cluster; chasing it.",
    "Freelancer budget for Q3 was approved for the web redesign.",
    "Incident review for last week's latency spike is scheduled; {p} is writing it up.",
    "Someone was paged at night for a disk alert that could have waited; we should fix it.",
    "The on-call schedule for next month is published.",
    "{p} is leaving the company at the end of the month; farewell lunch on Friday.",
    "Training budget requests for conferences are open until the end of the quarter.",
    "Deploys on Friday caused a scramble again; reminder to check the calendar.",
    "Database migration for the {t} schema went smoothly.",
    "We talked about team culture and how to give feedback in code review.",
]
WORK = [
    "Shipped the pagination fix for the sensor history endpoint.",
    "Closed {n} tickets from the backlog grooming session.",
    "Migrated the {t} dashboards to the new metrics backend.",
    "Load test reached {n}00 requests per second before saturating the database pool.",
    "Wrote the first draft of the firmware OTA rollback design.",
    "Paired with {p} on the alert routing rules.",
    "Reduced CI time for the {t} repository by {n} minutes.",
    "Investigated a memory leak in the mobile sync worker; fix in review.",
    "Updated the runbook links in the incident template.",
    "Retired two unused feature flags in the web app.",
]


def para(rng, pool, k):
    out = []
    for s in rng.sample(pool, k):
        out.append(s.format(p=rng.choice(PEOPLE), q=rng.choice(PEOPLE),
                            t=rng.choice(TEAMS), n=rng.randint(2, 9)))
    return " ".join(out)


def status(rng, i):
    team = rng.choice(TEAMS)
    week = f"2025-W{rng.randint(1, 30):02d}"
    return f"status/{team.replace(' ', '-')}-{week}-{i}.md", "\n\n".join([
        f"# {team} weekly status, {week}",
        "## Done\n\n" + para(rng, WORK, 4),
        "## Notes\n\n" + para(rng, NOISE, 4),
        "## Next\n\n" + para(rng, WORK, 3),
    ]) + "\n"


def meeting(rng, i):
    m, d = rng.randint(1, 9), rng.randint(1, 28)
    return f"meetings/2025-{m:02d}-{d:02d}-sync-{i}.md", "\n\n".join([
        f"# Cross-team sync, 2025-{m:02d}-{d:02d}",
        "Attendees: " + ", ".join(rng.sample(PEOPLE, 5)),
        "## Discussion\n\n" + para(rng, NOISE, 6),
        "## Updates\n\n" + para(rng, WORK, 4),
        "## Action items\n\n" + "\n".join(
            f"- {rng.choice(PEOPLE)}: {s}" for s in
            (x.format(p=rng.choice(PEOPLE), q=rng.choice(PEOPLE), t=rng.choice(TEAMS), n=3)
             for x in rng.sample(WORK, 3))),
    ]) + "\n"


def retro(rng, i):
    team = rng.choice(TEAMS)
    return f"retros/{team.replace(' ', '-')}-retro-{i}.md", "\n\n".join([
        f"# {team} sprint retro {i}",
        "## Went well\n\n" + para(rng, WORK, 3),
        "## Went badly\n\n" + para(rng, NOISE, 3),
        "## Try next\n\n" + para(rng, NOISE, 2),
    ]) + "\n"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("out")
    ap.add_argument("-n", type=int, default=300)
    a = ap.parse_args()
    rng = random.Random(20250314)
    kinds = [status, meeting, retro]
    for i in range(a.n):
        rel, text = kinds[i % 3](rng, i)
        p = Path(a.out) / "notes" / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)


if __name__ == "__main__":
    main()
