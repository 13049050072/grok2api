package provider

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
)

var registrationAPIKeyPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// RegistrationSSOIndex locates the SSO among the "----" separated fields of a
// registration-tool line
//
//	email----password----sso[----workosSession----workosXaiOrigin----workosId[----x-apikey]]
//
// where the fields after sso are optional and the password may itself contain
// "----". It resolves the position from the end of the line: a trailing
// x-apikey UUID fixes the full layout, otherwise the first JWT-shaped field
// among the possible SSO positions wins. ok is false when the fields do not
// determine the position; callers choose their own fallback.
func RegistrationSSOIndex(fields []string) (int, bool) {
	count := len(fields)
	switch {
	case count < 3:
		return 0, false
	case count == 3:
		return 2, true
	case count >= 7 && registrationAPIKeyPattern.MatchString(strings.TrimSpace(fields[count-1])):
		return count - 5, true
	}
	for index := max(2, count-5); index < count; index++ {
		if LooksLikeJWT(fields[index]) {
			return index, true
		}
	}
	return 0, false
}

// LooksLikeJWT reports whether value, optionally copied as a cookie
// ("sso=...; ..."), has the shape of a compact JWS: three base64url segments
// whose header decodes to a JSON object.
func LooksLikeJWT(value string) bool {
	value = strings.TrimSpace(value)
	if HasPrefixFoldASCII(value, "sso=") {
		value = strings.TrimSpace(value[len("sso="):])
	}
	value, _, _ = strings.Cut(value, ";")
	segments := strings.Split(strings.TrimSpace(value), ".")
	if len(segments) != 3 {
		return false
	}
	for _, segment := range segments {
		if segment == "" || strings.IndexFunc(segment, func(r rune) bool { return !isBase64URLRune(r) }) >= 0 {
			return false
		}
	}
	header, err := base64.RawURLEncoding.DecodeString(segments[0])
	if err != nil {
		return false
	}
	var object map[string]any
	return json.Unmarshal(header, &object) == nil && object != nil
}

func isBase64URLRune(r rune) bool {
	return ('A' <= r && r <= 'Z') || ('a' <= r && r <= 'z') || ('0' <= r && r <= '9') || r == '-' || r == '_'
}
