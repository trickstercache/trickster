package headers

var sensitiveCredentials = map[string]bool{NameAuthorization: true}

// HideAuthorizationCredentials replaces any sensitive HTTP header values with 5 asterisks
// sensitive headers are defined in the sensitiveCredentials map
func HideAuthorizationCredentials(headers Lookup) {
	// strip Authorization Headers
	for k := range headers {
		if _, ok := sensitiveCredentials[k]; ok {
			headers[k] = "*****"
		}
	}
}
