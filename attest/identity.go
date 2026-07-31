package attest

import (
	"context"
	"fmt"
)

// Who is acting, and what they may do.
//
// attest does not authenticate anyone. It is mounted into a host service that
// already knows who its users are, and asking it to grow a second, weaker
// notion of identity is how the two drift apart. So the host supplies a
// resolver at install time and attest calls it.
//
// TWO NAMES, not one, and this is the part that is easy to get wrong.
//
//   - The PRINCIPAL is the authenticated account, resolved from the request by
//     the host. A caller can never set it.
//   - The AUTHOR is the person who did the work, and it comes from the request,
//     because the person at the keyboard is routinely not the account holder.
//     An attorney hands a paralegal the link; the paralegal does the review.
//     That is normal, authorized, and the record has to be able to say so.
//
// Collapsing them loses the thing that matters. Recording only the principal
// files the paralegal's work under the attorney's name — a verdict in someone
// else's mouth. Recording only the author lets any holder of a link type any
// name with nothing behind it. Together they say what actually happened:
// authorized as X, performed by Y.
//
// The author is self-declared and therefore not proof of anything by itself.
// That is not a defect being tolerated — it is what a signature on a review
// sheet has always been. What the system guarantees is the other half: the
// principal cannot be forged, so the question "whose authority was this done
// under" always has a true answer.

// Permission is what a caller is asking to do.
//
// Three rather than read/write, because the public-serving case needs the
// middle one to exist on its own: an unattested transcript is worth showing to
// people who cannot rule on it, and a person authorised to rule on claims is
// not necessarily authorised to re-cut the machine's decomposition.
type Permission string

const (
	// PermRead — see the reading, the verdicts and the provenance.
	//
	// The public case. A transcript nobody has checked is still worth reading,
	// as long as it says so — which is what State.Provenance is for.
	PermRead Permission = "read"

	// PermAttest — record a verdict on a claim.
	PermAttest Permission = "attest"

	// PermResegment — change what the units ARE: join, split, re-cut.
	//
	// Separate from PermAttest because it is a stronger act. Ruling on a claim
	// leaves the machine's reading intact and disagreeable; resegmenting asserts
	// the decomposition itself was wrong, and every verdict on the retired units
	// stops applying.
	PermResegment Permission = "resegment"
)

// Identity is the host's answer to "which account is this, and may it do this".
//
// Both methods take the request context, because that is where a host keeps the
// authenticated caller. attest passes it through untouched and interprets
// nothing in it. Note what is NOT here: any notion of the person's name. See
// the delegation note above.
type Identity interface {
	// Principal is the authenticated account, written to Entry.Auth. Empty is
	// legal and means the mount is not account-based — a link, a loopback
	// binding — in which case the permission check is the whole of the
	// authorization and the record says so honestly rather than inventing an
	// account.
	Principal(ctx context.Context) (string, error)

	// Can reports whether this caller may do p to this asset. Asset-scoped
	// rather than global so a host can publish one corpus and restrict another
	// without mounting attest twice.
	Can(ctx context.Context, asset string, p Permission) (bool, error)
}

// Guest is the standalone CLI's identity: no account, everything permitted, and
// the reviewer still has to say who they are.
//
// It is the right stance for a tool bound to the loopback interface, and it is
// the same one oidio's and raglit's local servers already take. It is the wrong
// stance for anything reachable, which is why a host that mounts attest supplies
// its own rather than being offered a flag to loosen this one.
//
// "Authorized guest" is the honest description and it is what lands in the
// record: Auth is `guest`, so a reader can see that the ruling rests on nothing
// but access to the machine, and By is whoever typed their name.
type Guest struct{}

func (Guest) Principal(context.Context) (string, error) { return "guest", nil }

func (Guest) Can(context.Context, string, Permission) (bool, error) { return true, nil }

// ReadOnly wraps an identity so nothing can be written through it — the shape a
// host wants for a public mount, without having to reimplement Can.
type ReadOnly struct{ Identity }

func (r ReadOnly) Can(ctx context.Context, asset string, p Permission) (bool, error) {
	if p != PermRead {
		return false, nil
	}
	return r.Identity.Can(ctx, asset, p)
}

// signature resolves the pair that goes on an entry: the account the work was
// authorized under, and the person who says they did it.
//
// The author is refused when blank rather than defaulted. A defaulted author —
// the account name, the hostname, "unattributed" — reads afterwards exactly
// like a real one, and the first time it matters is the first time someone asks
// who performed a review and gets an answer that was never true.
func signature(ctx context.Context, id Identity, author string) (principal, by string, err error) {
	principal, err = id.Principal(ctx)
	if err != nil {
		return "", "", err
	}
	if author == "" {
		return "", "", fmt.Errorf("attest: name the person making this ruling — " +
			"the account it is authorized under is recorded separately, and is not a substitute")
	}
	return principal, author, nil
}
