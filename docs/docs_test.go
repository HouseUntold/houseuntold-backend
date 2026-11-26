package docs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSwaggerDocumentsEveryAPIOperation(t *testing.T) {
	var spec struct {
		BasePath            string                                `json:"basePath"`
		Paths               map[string]map[string]json.RawMessage `json:"paths"`
		SecurityDefinitions map[string]struct {
			Type string `json:"type"`
			In   string `json:"in"`
			Name string `json:"name"`
		} `json:"securityDefinitions"`
	}
	if err := json.Unmarshal(SwaggerJSON(), &spec); err != nil {
		t.Fatalf("parse generated swagger: %v", err)
	}
	if spec.BasePath != "/api" {
		t.Fatalf("basePath = %q, want /api", spec.BasePath)
	}

	want := []string{
		"GET /health",
		"GET /events",
		"GET /events/{id}",
		"GET /events/{id}/seats",
		"GET /events/{id}/seats/ws",
		"GET /tickets/{uuid}",
		"GET /tickets/{uuid}/image",
		"POST /checkout/init",
		"POST /checkout/{booking_id}/upi-reference",
		"POST /auth/login",
		"POST /auth/refresh",
		"POST /auth/logout",
		"POST /gate/validate",
		"POST /gate/check-in",
		"GET /counter/menu",
		"POST /counter/menu",
		"PUT /counter/menu/{id}",
		"DELETE /counter/menu/{id}",
		"GET /counter/search",
		"GET /counter/tickets/{bar_code}/history",
		"POST /counter/redeem",
		"POST /admin/staff",
		"GET /admin/staff",
		"PATCH /admin/staff/{id}/password",
		"DELETE /admin/staff/{id}",
		"GET /admin/bookings",
		"POST /admin/bookings/{id}/verify",
		"PATCH /admin/bookings/{id}/wa-sent",
		"GET /admin/tickets/search",
		"POST /admin/events",
		"GET /admin/events",
		"PUT /admin/events/{id}",
		"DELETE /admin/events/{id}",
		"PATCH /admin/events/{id}/first-99",
		"PATCH /admin/tiers/{id}",
		"PATCH /admin/tiers/{id}/price",
		"POST /admin/events/{id}/artists",
		"GET /admin/events/{id}/artists",
		"DELETE /admin/artists/{id}",
		"GET /admin/audit-logs",
		"POST /admin/events/{id}/tiers",
		"PUT /admin/events/{id}/seating",
		"GET /admin/stats",
		"GET /admin/stats/{event_id}",
		"GET /admin/ws",
		"POST /help",
		"GET /admin/help",
		"POST /admin/help/{id}/resolve",
	}
	for _, operation := range want {
		parts := strings.SplitN(operation, " ", 2)
		methods, ok := spec.Paths[parts[1]]
		if !ok {
			t.Fatalf("missing swagger path %s", parts[1])
		}
		if _, ok := methods[strings.ToLower(parts[0])]; !ok {
			t.Fatalf("missing swagger operation %s", operation)
		}
	}
	if len(want) != countSwaggerOperations(spec.Paths) {
		t.Fatalf("documented operations = %d, want %d", countSwaggerOperations(spec.Paths), len(want))
	}
	for _, name := range []string{"BearerAuth", "CookieAuth"} {
		if _, ok := spec.SecurityDefinitions[name]; !ok {
			t.Fatalf("missing security definition %s", name)
		}
	}
}

func TestSwaggerIndexHTMLIsEmbedded(t *testing.T) {
	html := string(SwaggerIndexHTML())
	for _, fragment := range []string{"swagger-ui.css", "swagger-ui-bundle.js", "SwaggerUIBundle", "/swagger/doc.json"} {
		if !strings.Contains(html, fragment) {
			t.Fatalf("embedded Swagger UI missing %q", fragment)
		}
	}
}

func countSwaggerOperations(paths map[string]map[string]json.RawMessage) int {
	count := 0
	for _, methods := range paths {
		count += len(methods)
	}
	return count
}
