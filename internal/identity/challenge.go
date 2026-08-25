package identity

// In-memory, single-use challenge store backing proof-of-possession on the
// account operations that need it (reveal, delete).
//
// In memory on purpose: a challenge is worthless once redeemed and worthless
// once expired, so persisting it would only widen the window in which a leaked
// one is useful. A meta-core restart invalidates every challenge in flight,
// which costs a client one retry.
//
// Mirrors the sign-in challenge in meta-watch's src/auth.rs, down to the TTL —
// the browser signs immediately, so seconds is the right order and a narrow
// window bounds replay if one ever surfaces in a proxy log.

import (
	"errors"
	"sync"
	"time"
)

// ChallengeTTL is how long an issued challenge stays answerable.
const ChallengeTTL = 120 * time.Second

// maxChallenges caps the store. An unauthenticated caller can mint challenges
// for any uid it can name, so the map is attacker-growable; without a ceiling
// that is a memory-exhaustion lever. At the cap, issuing fails rather than
// evicting: dropping someone else's live challenge on demand would let an
// attacker deny sign-outs, which is worse than making them retry.
const maxChallenges = 4096

var (
	// ErrChallengeUnknown covers absent, already-redeemed and expired alike.
	// One error for all three deliberately: distinguishing them tells a prober
	// whether a challenge string was ever real.
	ErrChallengeUnknown = errors.New("challenge is unknown, already used, or expired")
	ErrChallengeFull    = errors.New("too many challenges in flight; try again shortly")
)

type challengeEntry struct {
	uid     string
	action  Action
	expires time.Time
}

// ChallengeStore issues and redeems challenges. Safe for concurrent use.
type ChallengeStore struct {
	mu      sync.Mutex
	entries map[string]challengeEntry
	ttl     time.Duration
	// now is swappable so the tests can age entries without sleeping.
	now func() time.Time
}

func NewChallengeStore() *ChallengeStore {
	return &ChallengeStore{
		entries: make(map[string]challengeEntry),
		ttl:     ChallengeTTL,
		now:     time.Now,
	}
}

// Issue mints a challenge for (uid, action) and remembers it until redeemed or
// expired. Returns the literal text the client signs and its expiry.
func (s *ChallengeStore) Issue(uid string, action Action) (string, time.Time, error) {
	if !validUID(uid) {
		return "", time.Time{}, errors.New("invalid uid")
	}
	if !ValidAction(action) {
		return "", time.Time{}, errors.New("unknown action")
	}
	text, err := NewChallengeText(uid, action)
	if err != nil {
		return "", time.Time{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	if len(s.entries) >= maxChallenges {
		return "", time.Time{}, ErrChallengeFull
	}
	expires := s.now().Add(s.ttl)
	s.entries[text] = challengeEntry{uid: uid, action: action, expires: expires}
	return text, expires, nil
}

// Redeem consumes a challenge, asserting it was issued for this exact uid and
// action. Single-use: the entry is dropped on any lookup that finds it, so a
// mismatched redemption burns the challenge rather than leaving it to be
// retried against a different account.
func (s *ChallengeStore) Redeem(text, uid string, action Action) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[text]
	if !ok {
		return ErrChallengeUnknown
	}
	delete(s.entries, text)
	if s.now().After(entry.expires) {
		return ErrChallengeUnknown
	}
	if entry.uid != uid || entry.action != action {
		return ErrChallengeUnknown
	}
	// Re-read the signed bytes rather than trusting the map key alone: the
	// signature covers `text`, so `text` is what has to name this uid+action.
	if !ChallengeMatches(text, uid, action) {
		return ErrChallengeUnknown
	}
	return nil
}

// sweepLocked drops expired entries. Caller holds the lock.
//
// Called only from Issue: with no background goroutine, expiry is enforced
// lazily on the one path that grows the map, which is exactly where the bound
// matters.
func (s *ChallengeStore) sweepLocked() {
	now := s.now()
	for text, e := range s.entries {
		if now.After(e.expires) {
			delete(s.entries, text)
		}
	}
}
