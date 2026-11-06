# NOTES: the ledger operations in PostgreSQL
Table `ledger_entries(member_id, payment_id, kind, points, month)` with `UNIQUE (payment_id, kind)`. Each operation below is one transaction at READ COMMITTED (the default). Balance is `SELECT COALESCE(SUM(points),0) FROM ledger_entries WHERE member_id=$member`.
## Award(e, cap)
1. `SELECT pg_advisory_xact_lock(1, hashtext($member));` then `SELECT pg_advisory_xact_lock(2, hashtext($payment));`
2. `SELECT COALESCE(SUM(points),0) FROM ledger_entries WHERE member_id=$member AND month=$month AND kind='award';`
3. `INSERT INTO ledger_entries VALUES ($member, $payment, 'award', LEAST($points, GREATEST(cap - sum, 0)), $month) ON CONFLICT (payment_id, kind) DO NOTHING;`

## Reverse(payment)
1. `SELECT pg_advisory_xact_lock(2, hashtext($payment));`
2. `SELECT member_id, points, month FROM ledger_entries WHERE payment_id=$payment AND kind='award';` No row means an unknown payment: the handler acks and logs it, as the README says.
3. `INSERT INTO ledger_entries VALUES (member_id, $payment, 'reversal', -points, month) ON CONFLICT (payment_id, kind) DO NOTHING;` The row copies the awarded points, so a cap cut is reversed exactly.

## Why this stays correct with several replicas
- The advisory locks live in the database, so they serialize every award of one member (the cap) and every operation on one payment across all replicas and connections. Locks are always taken member first, then payment, and Reverse never takes the member lock, so there is no deadlock; `hashtext` collisions only add waiting.
- READ COMMITTED matters: each statement gets a fresh snapshot, so the SUM runs after the locks are granted and sees the previous holder's commit. Under REPEATABLE READ the snapshot would be taken by the lock statement while still waiting, and the cap could be exceeded.
- The unique index turns a redelivered payment or reversal into a no-op even when two replicas insert at once. A crash between COMMIT and Ack only redelivers a message whose insert now hits the index.
- If Award holds the payment lock, Reverse waits and then sees the committed award; if Reverse wins and finds no award, that result is final for that delivery.
- The handler nacks processing errors except unknown payment. A rollback leaves nothing behind and an uncertain COMMIT is retried safely, because both operations are idempotent.
- Indexes: `(member_id, month) WHERE kind='award'` for the SUM and `(member_id)` for Balance; the unique index already serves the Reverse lookup.
