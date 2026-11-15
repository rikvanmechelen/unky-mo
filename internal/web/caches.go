package web

import "time"

// caches are the server's short-lived memories of upstream answers: they
// decouple how often browsers poll from how often git, gh and Jira run.
type caches struct {
	prs     *ttlCache // gh pull requests
	tickets *ttlCache // the ticket providers
	// Files panel and nav, keyed by checkout path. Short TTLs: they only
	// dedupe several open tabs polling the same checkout.
	files    *ttlCache
	tree     *ttlCache
	log      *ttlCache
	overview *ttlCache
	arch     *ttlCache
	calls    *fingerprintCache
	// excerpt keeps annotated files for the Overview's code excerpts, keyed
	// by the change's revisions (and a working-tree file's stamp).
	excerpt *ttlCache
	// selection resolves Git log selections (see changeQuery).
	selection *ttlCache
	// Reviewer view: a branch's head commit (localRef) or a PR branch's,
	// fetched from origin at most every few minutes (fetch).
	localRef *ttlCache
	fetch    *ttlCache
	branch   *ttlCache
	shells   *ttlCache
	agents   *ttlCache
	commands *ttlCache
}

func newCaches(ticketRefresh time.Duration) caches {
	c := caches{
		prs:       newTTLCache(90 * time.Second),
		tickets:   newTTLCache(ticketRefresh),
		files:     newTTLCache(2 * time.Second),
		tree:      newTTLCache(10 * time.Second),
		log:       newTTLCache(2 * time.Second),
		overview:  newTTLCache(2 * time.Second),
		arch:      newTTLCache(3 * time.Second),
		calls:     newFingerprintCache(),
		excerpt:   newTTLCache(10 * time.Minute),
		selection: newTTLCache(10 * time.Minute),
		localRef:  newTTLCache(5 * time.Second),
		fetch:     newTTLCache(5 * time.Minute),
		branch:    newTTLCache(15 * time.Second),
		shells:    newTTLCache(2 * time.Second),
		agents:    newTTLCache(time.Second),
		commands:  newTTLCache(30 * time.Second),
	}
	// Annotated files are up to a few MB each: keep the most recent only.
	c.excerpt.maxEntries = 64
	return c
}
