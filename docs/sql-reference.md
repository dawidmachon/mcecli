# SFMC SQL reference — empirically validated on the reference org

Every construct below was tested against the platform via
`mcecli query validate` (server-side syntax check, zero side effects) on
Battery: 64 constructs validated via the server-side syntax check.
**Audience: agents writing Query Activity SQL.** Pair with `mcecli query run`.

## How the platform executes your SQL

`mcecli query run <key>` wraps your text as:
```sql
SET NOCOUNT ON; SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
INSERT INTO C{MID}.[{targetDE}] ([col1], [col2])
SELECT querydef.[col1], querydef.[col2] FROM ( {YOUR SELECT} ) AS querydef
SELECT @rcInsert = @@ROWCOUNT;
```
(Seen via `validatedQueryText` on the definition.) Consequences:
- **Only the SELECT is yours** — INSERT/INTO/DECLARE/EXEC are rejected
- **Exactly one statement** — `;` inside your text → "';' is a reserved word"
- Output column NAMES must match target DE columns (aliases define them)
- Views are context-prefixed `C{MID}.` — data views resolve per-MID

## Validated: grammar & clauses

| Construct | Status |
|---|---|
| `SELECT TOP n` | ✅ |
| `SELECT DISTINCT [TOP n]` | ✅ |
| column `AS` alias (alias = target column name) | ✅ |
| string/numeric literals, `+` string concat | ✅ |
| `WHERE` with `=  <>  !=  >  <  >=  <=` | ✅ |
| `AND  OR  NOT`, parentheses grouping | ✅ |
| `IN  NOT IN  LIKE  NOT LIKE  IS NULL  IS NOT NULL  BETWEEN` | ✅ |
| `GROUP BY` + `HAVING` | ✅ |
| `COUNT(*)  COUNT(DISTINCT col)  SUM  AVG  MIN  MAX` | ✅ |
| `INNER/LEFT/RIGHT/FULL OUTER/CROSS JOIN` (+ table aliases) | ✅ |
| 3+ table joins | ✅ |
| `UNION  UNION ALL` | ✅ |
| subquery in `FROM` (derived table + alias) | ✅ |
| subquery in `WHERE … IN (SELECT …)` | ✅ |
| `EXISTS` | ✅ |
| `ORDER BY [ASC/DESC]` (also with TOP) | ✅ accepted by validator |
| `ROW_NUMBER() OVER (PARTITION BY … ORDER BY …)` | ✅ |
| `--` and `/* */` comments | ✅ |
| `SELECT GETDATE()` (no FROM) | ✅ |

## Validated: functions

String: `CONCAT`, `LEN`, `SUBSTRING`, `LEFT`, `RIGHT`, `UPPER`, `LOWER`,
`LTRIM`, `RTRIM`, `CHARINDEX`, `REPLACE`, `ISNULL`, `COALESCE`
Numeric: `ABS`, `ROUND`, `FLOOR`, `CEILING`
Date: `GETDATE()`, `GETUTCDATE()`, `DATEADD(unit,n,date)`,
`DATEDIFF(unit,a,b)`, `DATEPART(unit,date)`, `YEAR`, `MONTH`, `DAY`
Types: `CAST(x AS type)`, `CONVERT(varchar(n), date, style)`
Misc: `CASE WHEN … THEN … ELSE … END`, `NEWID()`

## Rejected (validated)

| Construct | Platform answer |
|---|---|
| `;` (any second statement) | "';' is a reserved word and may not be used" |
| `DECLARE @var` | same reserved-word rejection — **no variables** |
| `INSERT INTO … SELECT` (explicit) | not allowed — platform inserts for you |
| `SELECT … INTO` | "'into' is a reserved word" |
| `EXEC …` | "Only SELECT queries are valid." |
| `LIKE` on DataExtensionObject retrieve (SOAP) | n/a — different API, no LIKE |

## Warnings (non-blocking)

Queries **without a WHERE clause** get the advisory "Just a heads-up…
Use a WHERE clause". Harmless; add a WHERE (even a tautology on purpose)
to keep validation output clean.

## Data views (queryable system tables)

Join keys: `_Sent.SubscriberID → _Subscribers.SubscriberID`,
`_Sent.JobID → _Job.JobID`, events tie to `_Job` via `JobID`+`BatchID`.

| View | Key columns (non-exhaustive) |
|---|---|
| `_Sent` | SubscriberID, SubscriberKey, EventDate, JobID, ListID, BatchID, Domain, TriggererSendDefinitionObjectID |
| `_Job` | JobID, EmailName, FromName, Subject, SendDate, FromEmail, characterSet |
| `_Open` | SubscriberID, EventDate, JobID, OpenCount, IsUnique |
| `_Click` | SubscriberID, EventDate, JobID, URL, LinkID, Clicks, IsUnique |
| `_Bounce` | SubscriberID, EventDate, JobID, SMTPBounceCategory, SMTPBounceReason, BounceType |
| `_Subscribers` | SubscriberID, SubscriberKey, EmailAddress, Status, DateJoined, LastModified |

Full column lists: the official data-views documentation, or
`mcecli de get <your-target-de>` for the target schema.

## Operational workflow (all read-only until run)

```
mcecli query validate --text "SELECT …" --target DE_KEY           # syntax pre-check (no gate)
mcecli rest POST automation/v1/queries --write --body @def.json   # create (queryText field!)
mcecli query run <key> --write --confirm                          # execute + poll
mcecli de rows <targetDE>                                         # read results
mcecli rest DELETE automation/v1/queries/{id} --write --confirm   # cleanup
```

## Context gotcha (learned the hard way)

`mcecli use` state is shared machine-wide and can be switched by another
session. A whole test batch once ran against the **PROD** profile
(read-only scopes — the 403s and write gates held) before anyone noticed.
**Always `mcecli status` before interpreting tenant-specific counts**, and
treat result-set differences between two sessions as a context switch
first, a platform anomaly second.
