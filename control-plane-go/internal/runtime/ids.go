package runtime

import "regexp"

var validServiceIDRE = regexp.MustCompile(`^[a-z0-9]{20,32}$`)

// ValidServiceID reports whether id has the shape of the cuids the API
// generates. A service id goes into a container label filter, a volume name, a
// route file name and a host path, so every destructive helper (and the
// teardown starter and activities) checks it with this one function.
func ValidServiceID(id string) bool { return validServiceIDRE.MatchString(id) }
