package db

import (
	"regexp"
	"testing"
)

// The built-in "delete class interface path" rule matches the entire tool_input JSON string, so the use case directly uses
// The JSON form is given, which is consistent with the subject actually obtained by the Interceptor.
func TestDeleteEndpointPathPattern(t *testing.T) {
	re := regexp.MustCompile(deleteEndpointPathPattern)

	hit := []string{
		`{"command":"curl -s 'http://t.com/api/user/delete?id=1'"}`,    // GET delete interface
		`{"command":"curl -X POST http://t.com/admin/delete -d id=1"}`, // POST delete interface
		`{"command":"curl 'http://t.com/api/deleteAll'"}`,
		`{"command":"curl 'http://t.com/api/delete_user?id=1'"}`,
		`{"command":"curl 'http://t.com/api/delete-user?id=1'"}`,
		`{"url":"http://t.com/api/remove?id=1"}`,
		`{"command":"curl http://t.com/files/unlink/3"}`,
		`{"command":"curl http://t.com/api/del?id=2"}`,
		`{"command":"curl -X POST http://t/v1/erase"}`,
		`{"command":"curl http://t/admin/destroyAll"}`, // The path rules of v1 do not allow suffixes, please add them here.
	}
	for _, s := range hit {
		if !re.MatchString(s) {
			t.Errorf("Should be hit but let go: %s", s)
		}
	}

	// The verb must be followed by a separator to prevent read-only paths such as /delivery and /details from being accidentally blocked.
	miss := []string{
		`{"command":"curl 'http://t.com/api/delivery?id=1'"}`,
		`{"command":"curl 'http://t.com/order/details'"}`,
		`{"command":"curl 'http://t.com/api/delta/sync'"}`,
		`{"command":"curl 'http://t.com/user/delegate'"}`,
		`{"command":"curl 'http://delete.example.com/'"}`, // Delete verb appears in domain name instead of path
		`{"command":"curl 'http://t.com/remote/status'"}`,
		`{"command":"nmap -p80 10.0.0.1"}`,
	}
	for _, s := range miss {
		if re.MatchString(s) {
			t.Errorf("Blocked by mistake: %s", s)
		}
	}
}
