//go:build ignore

// One-off smoke test for the admin console API (DESIGN §16).
// Usage: go run ./scripts/smoke_admin.go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

const (
	base      = "http://localhost:8080"
	adminBase = "/admin/api/x1" // 版本作用域：默认对 x1 后台做 smoke
)

var token string

func call(method, path string, body any, auth bool) (int, map[string]any) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("ERR", method, path, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

func step(name string, st int, m map[string]any) {
	fmt.Printf("[%d] %s -> %v\n", st, name, m)
}

func main() {
	// 1. login
	st, m := call("POST", "/admin/api/login", map[string]string{"username": "admin", "password": "admin12345"}, false)
	step("login", st, m)
	token, _ = m["token"].(string)
	if token == "" {
		fmt.Println("login failed, abort")
		os.Exit(1)
	}

	// 2. login with wrong password (expect 401)
	st, m = call("POST", "/admin/api/login", map[string]string{"username": "admin", "password": "wrong"}, false)
	step("login(wrong)", st, m)

	// 3. create user (name + mobile)
	st, m = call("POST", adminBase+"/users", map[string]any{
		"name": "测试商户A", "mobile": "13812345678",
	}, true)
	step("createUser", st, m)
	var licenseID string
	if u, ok := m["user"].(map[string]any); ok {
		licenseID, _ = u["licenseId"].(string)
	}

	// 4. list users
	st, m = call("GET", adminBase+"/users", nil, true)
	if arr, ok := m["users"].([]any); ok {
		fmt.Printf("[%d] listUsers -> %d users\n", st, len(arr))
	} else {
		step("listUsers", st, m)
	}

	// 4b. search users by mobile
	st, m = call("GET", adminBase+"/users?q=1381234", nil, true)
	if arr, ok := m["users"].([]any); ok {
		fmt.Printf("[%d] searchUsers(q=1381234) -> %d users\n", st, len(arr))
	} else {
		step("searchUsers", st, m)
	}

	// 5. update user (suspend + change mobile)
	st, m = call("PATCH", adminBase+"/users/"+licenseID, map[string]any{
		"status": "SUSPENDED", "mobile": "13900009999",
	}, true)
	step("updateUser", st, m)

	// 6. rotate secret
	st, m = call("POST", adminBase+"/users/"+licenseID+"/rotate-secret", nil, true)
	step("rotateSecret", st, m)

	// 7. audits (after a doCheck call below would populate; list now)
	st, m = call("GET", adminBase+"/audits?limit=10", nil, true)
	if arr, ok := m["audits"].([]any); ok {
		fmt.Printf("[%d] listAudits -> %d records\n", st, len(arr))
	} else {
		step("listAudits", st, m)
	}

	// 9. unauthorized access (expect 401)
	st, m = call("GET", adminBase+"/users", nil, false)
	step("listUsers(no token)", st, m)

	// 10. delete user
	st, m = call("DELETE", adminBase+"/users/"+licenseID, nil, true)
	step("deleteUser", st, m)

	fmt.Println("\nadmin smoke done.")
}
