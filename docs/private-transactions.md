# Private transaction lifetime

A privately admitted transaction is excluded from public peer announcements and
rebroadcasts for up to 30 minutes after its last successful private admission.
The store checks expiry every minute, so public eligibility can begin up to one
minute later. An in-flight private admission keeps its reservation until the
call completes.

After expiry, a transaction still in the pool becomes eligible for ordinary
public propagation. This fallback allows progress when all block producers
reject the transaction or relay retries are exhausted. Expiry does not remove
the transaction from the pool or shorten the pool's own retention policy.
Private submission therefore does not guarantee indefinite confidentiality.

Before expiry, resubmitting the same transaction through a public endpoint
returns `already known` if it is still in the pool, without adding it to the
public retry journal. If it has left the pool but still has a private marker,
public resubmission returns a private submission conflict. Markers for absent
transactions are swept after a two-minute grace period when pool presence can
be checked; the 30-minute expiry applies even without a pool checker.
