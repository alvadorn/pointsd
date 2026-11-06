# NOTES
Production store: Postgres table ledger_entries(member_id, payment_id, kind, points, month) with
UNIQUE (payment_id, kind). Each operation below is one transaction at READ COMMITTED (the default).
Award(e, cap): SELECT pg_advisory_xact_lock(1, hashtext($member)); SELECT pg_advisory_xact_lock(2, hashtext($payment));
SELECT COALESCE(SUM(points),0) FROM ledger_entries WHERE member_id=$member AND month=$month AND kind='award';
INSERT ... VALUES ($member, $payment, 'award', LEAST($points, GREATEST(cap - sum, 0)), $month)
ON CONFLICT (payment_id, kind) DO NOTHING. The locks live in the database, so they serialize every award of
one member (the cap) and every operation on one payment across all replicas and connections. READ COMMITTED
matters: each statement gets a fresh snapshot, so the SUM runs after the locks are granted and sees the previous
holder's commit (under REPEATABLE READ the snapshot would be taken by the lock statement while still waiting).
Reverse(payment): SELECT pg_advisory_xact_lock(2, hashtext($payment)); SELECT member_id, points, month FROM
ledger_entries WHERE payment_id=$payment AND kind='award' (no row: the handler acks and logs an unknown
payment, as README says); INSERT ... VALUES (member_id, $payment, 'reversal', -points, month) ON CONFLICT
(payment_id, kind) DO NOTHING, so a redelivered reversal is a no-op. The row copies the awarded points, so a
cap cut is reversed exactly. If Award holds the payment lock, Reverse waits; if Reverse wins, absence is final.
Balance: SELECT COALESCE(SUM(points),0) FROM ledger_entries WHERE member_id=$member. Retries are safe: the
handler nacks processing errors except unknown payment; a rollback leaves nothing behind, an uncertain COMMIT is
retried safely, and a crash before Ack redelivers a message whose insert now hits the unique index. Locks are
always taken member first, then payment, so no deadlock. Indexes: (member_id, month) WHERE kind='award', (member_id).
