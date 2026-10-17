# Retention

bunker never deletes anything on its own. Every `backup run` adds a snapshot,
so storage grows until you prune it. `bunker retention apply` does that: it
keeps a useful history and removes the rest.

```bash
bunker retention apply                  # preview every server, delete nothing
bunker retention apply my-vps           # preview one server
bunker retention apply my-vps --yes     # actually delete
```

## The policy

Set in `bunker.yaml` (these are the defaults):

```yaml
retention:
  daily: 7
  weekly: 4
  monthly: 6
```

Snapshots are walked from newest to oldest:

- **daily: 7**: keep the newest snapshot of each day, until 7 days are covered
- **weekly: 4**: keep the newest snapshot of each week (Monday to Sunday), until
  4 weeks are covered
- **monthly: 6**: keep the newest snapshot of each month, until 6 months are
  covered

A snapshot is kept if **any** rule keeps it. Everything else is deleted. When
a day has several snapshots, the newest one is the one kept.

## Worked example

Today is Saturday, October 17. There is a snapshot every day in October (two
on the 17th) plus a few older ones: 23 in total.

| Snapshot | Daily | Weekly | Monthly | Result |
|---|---|---|---|---|
| Oct 17 18:00 | day 1 | week 1 | Oct | **keep** |
| Oct 17 06:00 | same day, older | | | delete |
| Oct 16 to Oct 12 | days 2 to 6 | | | **keep** |
| Oct 11 (Sun) | day 7 | week 2 | | **keep** |
| Oct 10 to Oct 5 | 7 days used up | | | delete |
| Oct 4 (Sun) | | week 3 | | **keep** |
| Oct 3 to Oct 1 | | | | delete |
| Sep 20 (Sun) | | week 4 | Sep | **keep** |
| Sep 2 | | 4 weeks used up | Sep already kept | delete |
| Aug 15 | | | Aug | **keep** |
| Jul 1 | | | Jul | **keep** |
| Jan 10 | | | Jan | **keep** |

12 kept, 11 deleted: every day of the last week, one per week for a month,
and one per month beyond that.

## Days with a snapshot, not calendar days

In the example, January 10 is nine months old but still kept. The rules count
days, weeks and months **that have a snapshot**, not calendar time. There is
nothing from February to June, so January is only the fifth month counted.

This is deliberate. If your cron job breaks and no backups run for three weeks,
"the last 7 calendar days" would be empty and a calendar-based policy would
prune your most recent backups exactly when you need them. Counting snapshots
always keeps your last 7 good days.

## Safety

- **Preview by default.** Without `--yes` nothing is removed; you get the table
  and a total of what would be freed.
- **The newest snapshot is always kept**, whatever the policy says.
- **Only complete snapshots take part.** Folders shown as `no manifest` or
  `bad manifest` in `bunker backup list` are listed as `skip` and never
  deleted. Look at them and remove them by hand if they are junk.
- **Only configured servers.** Folders in storage that are not a server in
  `bunker.yaml` are never touched.
- Dates are in UTC, the same as snapshot names.

## Output

```
Policy: daily 7, weekly 4, monthly 6

my-vps: keep 4, delete 1
  SNAPSHOT             SIZE    ACTION  WHY
  2026-10-17T18-00-00  42 MiB  keep    daily 1, weekly 1, monthly 1
  2026-10-17T06-00-00  42 MiB  delete
  2026-10-16T09-00-00  42 MiB  keep    daily 2
  2026-10-08T13-32-59  41 MiB  keep    daily 3, weekly 2
  2026-09-29T17-01-50  40 MiB  keep    daily 4, weekly 3, monthly 2
  2026-09-20T10-00-00  -       skip    no manifest

Preview only: 1 snapshot(s), 42 MiB would be deleted. Nothing was removed.

Next: bunker retention apply my-vps --yes
```

`WHY` names every rule that keeps a snapshot. With `--yes`, the action column
shows `deleted` instead of `delete`.

Retention is not run automatically after a backup. To prune on a schedule, run
it from the same cron job or timer, after the backup:

```bash
bunker backup run --all && bunker retention apply --yes
```
