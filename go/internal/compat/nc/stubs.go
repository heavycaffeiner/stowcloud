//go:build compat_nc

package nc

// Endpoints that exist only so clients stop asking.
//
// Every one belongs to a feature this product declares a non-goal. Without
// them the client either retries in a loop or shows the user an error banner:
// a 404 on the notifications poll in particular makes the desktop client log
// an error every poll interval.
//
// The rule is an empty success, with the matching capability saying the
// feature is off so the client has no reason to read the payload.
//
// The exact shape is a client-crash workaround with a recorded cause, and it
// is ported verbatim rather than rationalised. An array and an object are not
// interchangeable to a typed client, and the four paths in NotFoundPaths must
// answer 404 rather than an empty success.

// Notifications answers the desktop client's poll.
func Notifications() Val { return VEmptyList() }

// UserStatuses answers the bulk status query.
func UserStatuses() Val { return VEmptyList() }

// NavigationApps answers the app list.
//
// An empty navigation list is what a server with no apps looks like, which is
// exactly what this is.
func NavigationApps() Val { return VEmptyList() }

// Autocomplete answers the sharee autocomplete.
//
// Deliberately always empty rather than wired to the principal directory: this
// endpoint has none of the rate limiting or minimum-length checks the sharee
// search has, so pointing it at real accounts would reopen the enumeration
// hole that search closes.
func Autocomplete() Val { return VEmptyList() }

// EmptyObject answers the provisioning config endpoints.
//
// An object, not a list. The client reads a record here and a list elsewhere,
// and the two are not interchangeable to its JSON layer.
func EmptyObject() Val { return VEmptyMap() }

// NotFoundPaths must answer 404 rather than an empty success.
//
// Answering 200 with an empty payload is worse than a 404: the client takes it
// as "supported but empty" and keeps polling. For the activity endpoints a 404
// is also the documented correct outcome, because the capabilities advertise
// the feature as absent.
//
// The singular user status endpoint used to answer an empty object, and that
// was worse. The Android client's status probe only special-cases a 404, from
// which it builds a safe synthetic offline status itself; anything else,
// including an empty object, it hands straight to its JSON layer. The status
// field is a non-nullable Kotlin field with no accessible no-arg constructor,
// so the JSON layer fills the empty object through Unsafe.allocateInstance and
// leaves that field null: a value the type system promised could not exist,
// waiting for the first dereference. A 404 exercises the client's own guarded
// path instead.
//
// A reimplementation that rationalises this into "200 with an empty body
// everywhere" reproduces a crash somebody already debugged.
func NotFoundPaths() []string {
	return []string{
		"/ocs/v2.php/apps/activity/api/v2/activity",
		"/ocs/v1.php/apps/activity/api/v2/activity",
		"/ocs/v2.php/apps/user_status/api/v1/user_status",
		"/ocs/v1.php/apps/user_status/api/v1/user_status",
	}
}

// IsNotFoundPath reports whether a path is in that set.
func IsNotFoundPath(p string) bool {
	for _, want := range NotFoundPaths() {
		if p == want {
			return true
		}
	}
	return false
}
