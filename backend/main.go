package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
)

type M = map[string]any
type State map[string][]M

var db *sql.DB

func str(m M, k string) string { v, _ := m[k].(string); return v }
func number(m M, k string) int64 {
	switch v := m[k].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}
func obj(m M, k string) M {
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	return M{}
}
func arr(m M, k string) []any { v, _ := m[k].([]any); return v }
func contains(a []any, v string) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}
func find(a []M, id string) M {
	for _, m := range a {
		if str(m, "id") == id {
			return m
		}
	}
	return nil
}
func uuid() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func now() string          { return time.Now().UTC().Format(time.RFC3339Nano) }
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func jsonBody(m M) string  { b, _ := json.Marshal(m); return string(b) }
func admin(u M) bool       { v, _ := u["superuser"].(bool); return v }
func assigned(s State, u M, t string) []M {
	var out []M
	for _, m := range s["memberships"] {
		if str(m, "tenantId") == t && str(m, "userId") == str(u, "id") {
			for _, r := range s["roles"] {
				if contains(arr(m, "roleIds"), str(r, "id")) {
					out = append(out, r)
				}
			}
		}
	}
	return out
}
func can(s State, u M, t, action, form string) bool {
	if admin(u) {
		return true
	}
	for _, r := range assigned(s, u, t) {
		if contains(arr(obj(obj(r, "permissions"), form), "actions"), action) {
			return true
		}
	}
	return false
}
func access(s State, u M, t, key, form string) string {
	if admin(u) {
		return "edit"
	}
	v := "hidden"
	for _, r := range assigned(s, u, t) {
		a := str(obj(obj(obj(r, "permissions"), form), "fields"), key)
		if a == "edit" {
			return a
		}
		if a == "view" {
			v = a
		}
	}
	return v
}
func hasStage(s State, u M, t, stage string) bool {
	if admin(u) {
		return true
	}
	for _, r := range assigned(s, u, t) {
		if str(r, "stage") == stage {
			return true
		}
	}
	return false
}
func status(s State, a M) string {
	events := []M{}
	for _, e := range s["events"] {
		if str(e, "amendmentId") == str(a, "id") {
			events = append(events, e)
		}
	}
	return financialStatus(a, events)
}
func effective(s State, o M) int64 {
	if orderStatus(s, o) != "Approved" {
		return 0
	}
	n := number(o, "base")
	for _, a := range s["amendments"] {
		if str(a, "orderId") == str(o, "id") && status(s, a) == "Approved" {
			n += number(a, "delta")
		}
	}
	return n
}
func latest(s State, id string) M {
	var f M
	for _, x := range s["forms"] {
		if str(x, "id") == id && (f == nil || number(x, "version") > number(f, "version")) {
			f = x
		}
	}
	return f
}
func loadState(tx *sql.Tx) (State, error) {
	s := State{}
	queries := map[string]string{
		"extensions":      "SELECT body FROM erp_extensions ORDER BY (body->>'createdAt')::timestamptz,id",
		"extensionEvents": "SELECT body FROM erp_extension_events ORDER BY (body->>'at')::timestamptz,id",
		"suiteRecords":    "SELECT body FROM operational_records ORDER BY (body->>'createdAt')::timestamptz,id",
		"suiteEvents":     "SELECT body FROM operational_events ORDER BY (body->>'at')::timestamptz,id",
		"orderEvents":     "SELECT body FROM work_order_events ORDER BY (body->>'at')::timestamptz,id",
		"expenses":        "SELECT body FROM finance_documents WHERE kind='expense' ORDER BY body->>'createdAt',id",
		"payments":        "SELECT body FROM finance_documents WHERE kind='payment' ORDER BY body->>'createdAt',id",
		"invoices":        "SELECT body FROM vendor_invoices ORDER BY invoice_date,id",
		"financeEvents":   "SELECT body FROM finance_events ORDER BY (body->>'at')::timestamptz,id",
		"invoiceEvents":   "SELECT body FROM invoice_events ORDER BY (body->>'at')::timestamptz,id",
		"users":           "SELECT jsonb_build_object('id',id,'name',name,'title',title,'superuser',superuser,'username',username) FROM users ORDER BY id",
		"tenants":         "SELECT body || jsonb_build_object('deleted',deleted) FROM tenants ORDER BY code",
		"reras":           "SELECT body FROM rera_accounts ORDER BY number",
		"roles":           "SELECT body FROM roles ORDER BY id",
		"memberships":     "SELECT jsonb_build_object('tenantId',m.tenant_id,'userId',m.user_id,'roleIds',coalesce((SELECT jsonb_agg(role_id ORDER BY role_id) FROM membership_roles r WHERE r.tenant_id=m.tenant_id AND r.user_id=m.user_id),'[]'::jsonb)) FROM memberships m ORDER BY tenant_id,user_id",
		"forms":           "SELECT body FROM form_versions ORDER BY id,version",
		"orders":          "SELECT w.body || coalesce((SELECT jsonb_build_object('vendorId',v.vendor_id,'vendorSnapshot',v.snapshot) FROM work_order_vendors v WHERE v.order_id=w.id),'{}'::jsonb) FROM work_orders w ORDER BY w.body->>'createdAt',w.id",
		"vendors":         "SELECT body FROM vendors ORDER BY body->>'name',id",
		"amendments":      "SELECT body || jsonb_build_object('approvalPolicy',approval_policy,'creatorSuperuser',creator_superuser) FROM amendments ORDER BY body->>'createdAt',id",
		"events":          "SELECT body FROM amendment_events ORDER BY body->>'at',id",
		"audit":           "SELECT body FROM audit_log ORDER BY body->>'at',id",
		"purchaseOrders":  "SELECT body || jsonb_build_object('orderDate',coalesce(order_date::text,substring(body->>'createdAt',1,10)),'approvalPolicy',approval_policy,'creatorSuperuser',creator_superuser) FROM purchase_orders ORDER BY body->>'createdAt',id",
		"purchaseEvents":  "SELECT body FROM purchase_events ORDER BY body->>'at',id",
		"stockLots":       "SELECT body FROM stock_lots ORDER BY id",
		"stockMovements":  "SELECT body FROM stock_movements ORDER BY body->>'at',id",
		"uploads":         "SELECT body - 'csv' FROM uploads ORDER BY id",
	}
	for key, q := range queries {
		s[key] = []M{}
		rows, err := tx.Query(q)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var b []byte
			if err = rows.Scan(&b); err != nil {
				rows.Close()
				return nil, err
			}
			var m M
			if err = json.Unmarshal(b, &m); err != nil {
				rows.Close()
				return nil, err
			}
			s[key] = append(s[key], m)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}
func redact(s State, u M) State {
	for _, p := range s["tenants"] {
		for k := range obj(p, "data") {
			if access(s, u, str(p, "id"), k, "project") == "hidden" {
				delete(obj(p, "data"), k)
			}
		}
	}
	s["dashboards"] = dashboardSummary(s, u)
	s["vendorReports"] = vendorReports(s, u)
	redactFinance(s, u)
	redactSuite(s, u)
	redactExtensions(s, u)
	for _, p := range s["purchaseOrders"] {
		redactItemExtras(s, u, str(p, "tenantId"), "purchase_order", arr(p, "lines"))
	}
	redactProcurement(s, u)
	vendors := []M{}
	for _, v := range s["vendors"] {
		if redacted := redactVendor(s, u, str(v, "tenantId"), v); redacted != nil {
			vendors = append(vendors, redacted)
		}
	}
	s["vendors"] = vendors
	banks := []M{}
	for _, r := range s["reras"] {
		t := str(r, "tenantId")
		if !can(s, u, t, "view", "rera_account") {
			continue
		}
		for _, key := range []string{"number", "bank", "ifsc", "label"} {
			if access(s, u, t, key, "rera_account") == "hidden" {
				delete(r, key)
			}
		}
		for k := range obj(r, "data") {
			if access(s, u, t, k, "rera_account") == "hidden" {
				delete(obj(r, "data"), k)
			}
		}
		banks = append(banks, r)
	}
	s["reras"] = banks
	orders := []M{}
	visible := map[string]M{}
	for _, o := range s["orders"] {
		t := str(o, "tenantId")
		f := str(o, "formId")
		redactWorkDetails(s, u, o)
		if !can(s, u, t, "view", f) {
			continue
		}
		if access(s, u, t, "amount", f) == "hidden" {
			delete(o, "base")
		}
		if access(s, u, t, "description", f) == "hidden" {
			delete(o, "description")
		}
		if snapshot := redactVendorSnapshot(s, u, t, obj(o, "vendorSnapshot")); snapshot == nil {
			delete(o, "vendorSnapshot")
		} else {
			o["vendorSnapshot"] = snapshot
		}
		for k := range obj(o, "data") {
			if access(s, u, t, k, f) == "hidden" {
				delete(obj(o, "data"), k)
			}
		}
		orders = append(orders, o)
		visible[str(o, "id")] = o
	}
	s["orders"] = orders
	amendments := []M{}
	visibleA := map[string]bool{}
	for _, a := range s["amendments"] {
		o := visible[str(a, "orderId")]
		if o == nil {
			continue
		}
		t, f := str(o, "tenantId"), str(o, "formId")
		if access(s, u, t, "amount", f) == "hidden" {
			delete(a, "delta")
		}
		if access(s, u, t, "reason", f) == "hidden" {
			delete(a, "reason")
		}
		for k := range obj(a, "data") {
			if access(s, u, t, k, f) == "hidden" {
				delete(obj(a, "data"), k)
			}
		}
		amendments = append(amendments, a)
		visibleA[str(a, "id")] = true
	}
	s["amendments"] = amendments
	events := []M{}
	for _, e := range s["events"] {
		if visibleA[str(e, "amendmentId")] {
			events = append(events, e)
		}
	}
	s["events"] = events
	if !admin(u) {
		s["uploads"] = []M{}
		logs := []M{}
		for _, a := range s["audit"] {
			if can(s, u, str(a, "tenantId"), "view", "work_order") || can(s, u, str(a, "tenantId"), "view", "vendor_onboarding") {
				delete(a, "before")
				delete(a, "after")
				logs = append(logs, a)
			}
		}
		s["audit"] = logs
		people := map[string]bool{str(u, "id"): true}
		for _, key := range []string{"expenses", "payments", "invoices", "suiteRecords"} {
			for _, d := range s[key] {
				people[str(d, "createdBy")] = true
			}
		}
		for _, key := range []string{"financeEvents", "invoiceEvents", "suiteEvents", "orderEvents"} {
			for _, e := range s[key] {
				people[str(e, "actorId")] = true
			}
		}
		for _, m := range s["memberships"] {
			people[str(m, "userId")] = true
		}
		for _, o := range s["orders"] {
			people[str(o, "createdBy")] = true
		}
		for _, a := range s["amendments"] {
			people[str(a, "createdBy")] = true
		}
		for _, e := range s["events"] {
			people[str(e, "actorId")] = true
		}
		for _, p := range s["purchaseOrders"] {
			people[str(p, "createdBy")] = true
		}
		for _, e := range s["purchaseEvents"] {
			people[str(e, "actorId")] = true
		}
		for _, m := range s["stockMovements"] {
			people[str(m, "actorId")] = true
		}
		for _, a := range s["audit"] {
			people[str(a, "actorId")] = true
		}
		users := []M{}
		for _, person := range s["users"] {
			if people[str(person, "id")] {
				if str(person, "id") != str(u, "id") {
					delete(person, "username")
				}
				users = append(users, person)
			}
		}
		s["users"] = users
	}
	return s
}
func transaction(user string) (*sql.Tx, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec("SELECT set_config('app.user_id',$1,true)", user); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}
func authenticated(r *http.Request) (string, error) {
	c, err := r.Cookie("ledger_session")
	if err != nil {
		return "", errors.New("Sign in to continue")
	}
	var id string
	err = db.QueryRow("SELECT user_id FROM sessions WHERE token_hash=$1 AND expires_at>now()", hash(c.Value)).Scan(&id)
	if err != nil {
		return "", errors.New("Session expired. Sign in again")
	}
	return id, nil
}
func write(w http.ResponseWriter, status int, b any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(b)
}
func fail(w http.ResponseWriter, err error) {
	var pe *pq.Error
	if errors.As(err, &pe) {
		log.Printf("database error: %s %s", pe.Code, pe.Message)
		msg := "The operation could not be saved."
		switch pe.Code {
		case "23505":
			if pe.Constraint == "unique_username" {
				msg = "Username already exists."
			} else {
				msg = "This record already exists. RERA accounts must be globally unique."
			}
		case "P0001":
			msg = pe.Message
		case "23514":
			msg = "Values do not satisfy the record constraints."
		case "42501":
			msg = "Your account does not have access to this project."
		}
		write(w, 409, M{"error": msg})
		return
	}
	write(w, 400, M{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2000000)
	d := json.NewDecoder(r.Body)
	d.UseNumber()
	if err := d.Decode(out); err != nil {
		return errors.New("Invalid JSON request or request is too large")
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return errors.New("Only one JSON object is allowed")
	}
	return nil
}
func originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

var loginMu sync.Mutex
var loginRates = map[string]struct {
	count int
	since time.Time
}{}

func rateOK(key string) bool {
	loginMu.Lock()
	defer loginMu.Unlock()
	v := loginRates[key]
	if time.Since(v.since) > time.Minute {
		v.count = 0
		v.since = time.Now()
	}
	v.count++
	loginRates[key] = v
	return v.count <= 15
}
func serveAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == "POST" && !originOK(r) {
		write(w, 403, M{"error": "Request origin is not allowed"})
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/")
	if p == "health" {
		if err := db.Ping(); err != nil {
			write(w, 503, M{"error": "Database unavailable"})
		} else {
			write(w, 200, M{"ok": true})
		}
		return
	}
	if p == "login" && r.Method == "POST" {
		if !rateOK(strings.Split(r.RemoteAddr, ":")[0]) {
			write(w, 429, M{"error": "Too many login attempts. Try again in a minute."})
			return
		}
		var b M
		if err := decode(w, r, &b); err != nil {
			fail(w, err)
			return
		}
		var id string
		err := db.QueryRow("SELECT id FROM users WHERE (id=$1 OR lower(username)=lower($1)) AND password_hash=crypt($2,password_hash)", str(b, "userId"), str(b, "password")).Scan(&id)
		if err != nil {
			write(w, 401, M{"error": "Account or password is incorrect"})
			return
		}
		token := uuid() + uuid()
		_, err = db.Exec("INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '12 hours')", hash(token), id)
		if err != nil {
			fail(w, err)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "ledger_session", Value: token, Path: "/", HttpOnly: true, Secure: os.Getenv("COOKIE_SECURE") == "true", SameSite: http.SameSiteStrictMode, MaxAge: 43200})
		write(w, 200, M{"userId": id})
		return
	}
	id, err := authenticated(r)
	if err != nil {
		write(w, 401, M{"error": err.Error()})
		return
	}
	if p == "logout" && r.Method == "POST" {
		if c, e := r.Cookie("ledger_session"); e == nil {
			db.Exec("DELETE FROM sessions WHERE token_hash=$1", hash(c.Value))
		}
		http.SetCookie(w, &http.Cookie{Name: "ledger_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		write(w, 200, M{"ok": true})
		return
	}
	tx, err := transaction(id)
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback()
	if strings.HasPrefix(p, "boq/") && r.Method == "GET" {
		serveBOQ(w, r, tx, strings.TrimPrefix(p, "boq/"), id)
		return
	}
	if p == "state" && r.Method == "GET" {
		s, err := loadState(tx)
		if err != nil {
			fail(w, err)
			return
		}
		u := find(s["users"], id)
		redact(s, u)
		result := M{}
		for k, v := range s {
			result[k] = v
		}
		result["currentUserId"] = id
		write(w, 200, result)
		return
	}
	if p == "command" && r.Method == "POST" {
		var b M
		if err = decode(w, r, &b); err != nil {
			fail(w, err)
			return
		}
		t := str(b, "tenantId")
		var allowed bool
		if err = tx.QueryRow("SELECT has_tenant($1)", t).Scan(&allowed); err != nil || !allowed {
			write(w, 403, M{"error": "Your account has no access to this project"})
			return
		}
		kind := str(b, "kind")
		d := obj(b, "data")
		config := contains([]any{"role", "role-delete", "membership", "form", "tenant", "tenant-delete", "rera", "extension-save", "user-create", "user-credentials"}, kind)
		lockSQL := "SELECT pg_advisory_xact_lock_shared(7249135)"
		if config {
			lockSQL = "SELECT pg_advisory_xact_lock(7249135)"
		}
		if _, err = tx.Exec(lockSQL); err != nil {
			fail(w, err)
			return
		}
		projects := []string{t}
		if kind == "inventory-transfer" {
			projects = append(projects, str(d, "targetTenantId"))
		}
		if kind == "tenant" || kind == "tenant-delete" {
			projects = append(projects, str(d, "id"))
		}
		sort.Strings(projects)
		previous := ""
		for _, project := range projects {
			if project == "" || project == previous {
				continue
			}
			previous = project
			if _, err = tx.Exec("SELECT pg_advisory_xact_lock(hashtext($1))", project); err != nil {
				fail(w, err)
				return
			}
		}
		s, err := loadState(tx)
		if err != nil {
			fail(w, err)
			return
		}
		u := find(s["users"], id)
		if t == "" && admin(u) && contains([]any{"role", "role-delete", "form", "user-create", "user-credentials"}, kind) && len(s["tenants"]) > 0 {
			t = str(s["tenants"][0], "id")
		}
		result, err := mutate(tx, s, u, t, kind, d)
		if err != nil {
			fail(w, err)
			return
		}
		if err = tx.Commit(); err != nil {
			fail(w, err)
			return
		}
		write(w, 200, result)
		return
	}
	write(w, 404, M{"error": "Endpoint not found"})
}
func logAudit(tx *sql.Tx, u M, t, action, target string) error {
	m := M{"id": uuid(), "tenantId": t, "actorId": str(u, "id"), "action": action, "target": target, "at": now()}
	_, err := tx.Exec("INSERT INTO audit_log(id,tenant_id,actor_id,body) VALUES($1,$2,$3,$4)", str(m, "id"), t, str(u, "id"), jsonBody(m))
	return err
}
func putAmend(tx *sql.Tx, u M, o M, delta int64, reason, upload string) error {
	m := M{"id": uuid(), "orderId": str(o, "id"), "delta": delta, "reason": reason, "data": obj(o, "data"), "formVersion": number(o, "formVersion"), "createdBy": str(u, "id"), "createdAt": now(), "approvalPolicy": 2, "creatorSuperuser": admin(u)}
	var up any
	if upload != "" {
		m["uploadId"] = upload
		up = upload
	}
	_, err := tx.Exec("INSERT INTO amendments(id,order_id,delta,created_by,upload_id,body) VALUES($1,$2,$3,$4,$5,$6)", str(m, "id"), str(o, "id"), delta, str(u, "id"), up, jsonBody(m))
	return err
}

var decimalPattern = regexp.MustCompile(`^[+-]?\d+(\.\d{1,2})?$`)

func cents(text string) (int64, error) {
	text = strings.TrimSpace(text)
	if !decimalPattern.MatchString(text) {
		return 0, errors.New("Use a decimal amount with at most two decimal places")
	}
	neg := strings.HasPrefix(text, "-")
	text = strings.TrimLeft(text, "+-")
	p := strings.Split(text, ".")
	whole, err := strconv.ParseInt(p[0], 10, 64)
	if err != nil || whole > 1000000000000 {
		return 0, errors.New("Amount is outside the supported range")
	}
	var fractional int64
	if len(p) == 2 {
		digits := p[1]
		if len(digits) == 1 {
			digits += "0"
		}
		fractional, _ = strconv.ParseInt(digits, 10, 64)
	}
	n := whole*100 + fractional
	if neg {
		n = -n
	}
	if n > 100000000000000 || n < -100000000000000 {
		return 0, errors.New("Amount is outside the supported range")
	}
	return n, nil
}
func validAmount(d M, key string, zero bool) (int64, error) {
	raw, ok := d[key]
	if !ok {
		return 0, errors.New("Financial amount is required")
	}
	var n int64
	switch v := raw.(type) {
	case json.Number:
		x, e := v.Int64()
		if e != nil {
			return 0, errors.New("Amount must be integer paise")
		}
		n = x
	case int64:
		n = v
	default:
		return 0, errors.New("Amount must be integer paise")
	}
	if n > 100000000000000 || n < -100000000000000 || (!zero && n == 0) {
		return 0, errors.New("Amount is zero or outside the supported range")
	}
	return n, nil
}
func previewCSV(s State, u M, t, text string) ([]M, []M, error) {
	if !can(s, u, t, "amend", "work_order") || access(s, u, t, "amount", "work_order") != "edit" || access(s, u, t, "reason", "work_order") != "edit" {
		return nil, nil, errors.New("Your roles do not allow financial imports")
	}
	if len(text) > 1000000 {
		return nil, nil, errors.New("CSV must be smaller than 1 MB")
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(text, "\ufeff")))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, nil, errors.New("CSV could not be parsed: " + err.Error())
	}
	if len(records) < 2 || len(records) > 501 {
		return nil, nil, errors.New("CSV needs a header and 1–500 rows")
	}
	header := records[0]
	for i := range header {
		header[i] = strings.TrimSpace(header[i])
	}
	if strings.Join(header, ",") != "work_order,delta_including_tax,reason" {
		return nil, nil, errors.New("Expected columns: work_order,delta_including_tax,reason")
	}
	out, issues := []M{}, []M{}
	totals := map[string]int64{}
	for i, r := range records[1:] {
		var rowErr error
		var o M
		var delta, previous, next int64
		if len(r) != 3 {
			rowErr = errors.New("Expected three columns")
		} else {
			for _, w := range s["orders"] {
				if str(w, "tenantId") == t && str(w, "number") == strings.TrimSpace(r[0]) {
					o = w
					break
				}
			}
			if o == nil {
				rowErr = errors.New("Work order does not exist in this project")
			} else if delta, err = cents(r[1]); err != nil {
				rowErr = err
			} else if delta == 0 {
				rowErr = errors.New("Amendment cannot be zero")
			} else if strings.TrimSpace(r[2]) == "" {
				rowErr = errors.New("Reason is required")
			} else {
				var exists bool
				previous, exists = totals[str(o, "id")]
				if !exists {
					previous = effective(s, o)
				}
				next = previous + delta
				if next < 0 || next > 100000000000000 {
					rowErr = errors.New("Resulting total would be below zero or outside the supported range")
				}
			}
		}
		if rowErr != nil {
			issues = append(issues, M{"row": i + 2, "message": rowErr.Error()})
			continue
		}
		totals[str(o, "id")] = next
		out = append(out, M{"orderId": str(o, "id"), "number": str(o, "number"), "delta": delta, "reason": strings.TrimSpace(r[2]), "previous": previous, "next": next})
	}
	return out, issues, nil
}
func mutate(tx *sql.Tx, s State, u M, t, kind string, d M) (M, error) {
	if kind == "tenant" || kind == "tenant-delete" {
		return manageProject(tx, s, u, kind, d)
	}
	if !contains([]any{"user-create", "user-credentials", "role", "role-delete", "form"}, kind) {
		p := find(s["tenants"], t)
		if p == nil || p["deleted"] == true {
			return nil, errors.New("Select an active project")
		}
	}
	if contains([]any{"purchase-order", "purchase-transition", "purchase-receive", "inventory-transfer", "inventory-issue"}, kind) {
		return manageProcurement(tx, s, u, t, kind, d)
	}
	if contains([]any{"expense", "payment", "invoice", "finance-transition", "invoice-status"}, kind) {
		return manageFinance(tx, s, u, t, kind, d)
	}
	if contains([]any{"onboarding-save", "onboarding-transition"}, kind) {
		return manageOnboarding(tx, s, u, t, kind, d)
	}
	if kind == "extension-save" || kind == "extension-transition" {
		return manageExtensions(tx, s, u, t, kind, d)
	}
	if contains([]any{"suite-save", "suite-transition", "order-transition"}, kind) {
		return manageSuite(tx, s, u, t, kind, d)
	}
	id := str(u, "id")
	if contains([]any{"vendor", "vendor-delete", "user-create", "user-credentials"}, kind) {
		return manageVendorUser(tx, s, u, t, kind, d)
	}
	ok := M{"ok": true}
	deny := func() error { return errors.New("Your roles do not allow this action") }
	switch kind {
	case "preview", "import":
		rows, issues, err := previewCSV(s, u, t, str(d, "csv"))
		if err != nil {
			return nil, err
		}
		if kind == "preview" {
			return M{"rows": rows, "errors": issues}, nil
		}
		if len(issues) > 0 {
			return nil, errors.New("Entire upload rejected. Correct all validation errors first")
		}
		up := M{"id": uuid(), "tenantId": t, "createdBy": id, "createdAt": now(), "filename": str(d, "filename"), "csv": str(d, "csv"), "validationStatus": "accepted"}
		_, err = tx.Exec("INSERT INTO uploads(id,tenant_id,created_by,checksum,body) VALUES($1,$2,$3,$4,$5)", str(up, "id"), t, id, hash(str(d, "csv")), jsonBody(up))
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if err = putAmend(tx, u, find(s["orders"], str(r, "orderId")), number(r, "delta"), str(r, "reason"), str(up, "id")); err != nil {
				return nil, err
			}
		}
		return ok, logAudit(tx, u, t, "CSV imported as draft amendments", str(d, "filename"))
	case "amend":
		o := find(s["orders"], str(d, "orderId"))
		if o == nil || str(o, "tenantId") != t {
			return nil, errors.New("Work order not found")
		}
		f := str(o, "formId")
		if !can(s, u, t, "amend", f) || access(s, u, t, "amount", f) != "edit" || access(s, u, t, "reason", f) != "edit" {
			return nil, deny()
		}
		delta, err := validAmount(d, "delta", false)
		if err != nil {
			return nil, err
		}
		n := effective(s, o) + delta
		if n < 0 || n > 100000000000000 {
			return nil, errors.New("Resulting total must be at least zero and within the supported range")
		}
		reason := strings.TrimSpace(str(d, "reason"))
		if reason == "" {
			return nil, errors.New("Reason is required")
		}
		if err = putAmend(tx, u, o, delta, reason, ""); err != nil {
			return nil, err
		}
		return ok, logAudit(tx, u, t, "Draft amendment created", str(o, "number"))
	case "order":
		f := latest(s, str(d, "formId"))
		if f == nil || str(f, "id") != "work_order" {
			return nil, errors.New("Choose the work order template")
		}
		if !can(s, u, t, "create", "work_order") || access(s, u, t, "amount", "work_order") != "edit" || access(s, u, t, "description", "work_order") != "edit" {
			return nil, deny()
		}
		base, err := validAmount(d, "base", true)
		if err != nil || base < 0 {
			return nil, errors.New("Original total must be non-negative integer paise")
		}
		vendor := find(s["vendors"], str(d, "vendorId"))
		if vendor == nil || str(vendor, "tenantId") != t || vendor["deleted"] == true || (str(vendor, "onboardingStatus") != "" && str(vendor, "onboardingStatus") != "Active") {
			return nil, errors.New("Select an active vendor in this project")
		}
		d["vendor"] = str(vendor, "name")
		for _, k := range []string{"number", "vendor", "description"} {
			d[k] = strings.TrimSpace(str(d, k))
			if str(d, k) == "" {
				return nil, errors.New(k + " is required")
			}
		}
		data := obj(d, "data")
		known := map[string]bool{}
		for _, raw := range arr(f, "fields") {
			field, _ := raw.(map[string]any)
			key := str(field, "key")
			known[key] = true
			value := str(data, key)
			required, _ := field["required"].(bool)
			if required && strings.TrimSpace(value) == "" {
				return nil, errors.New(str(field, "label") + " is required")
			}
			if value != "" && access(s, u, t, key, "work_order") != "edit" {
				return nil, deny()
			}
			if str(field, "type") == "number" && value != "" {
				if _, err = strconv.ParseFloat(value, 64); err != nil {
					return nil, errors.New(str(field, "label") + " must be a number")
				}
			}
			if str(field, "type") == "date" && value != "" {
				if _, err = time.Parse("2006-01-02", value); err != nil {
					return nil, errors.New(str(field, "label") + " must be a date")
				}
			}
		}
		for k := range data {
			if !known[k] {
				return nil, errors.New("Unknown custom field: " + k)
			}
		}
		contract, err := workContract(s, u, t, d)
		if err != nil {
			return nil, err
		}
		details, err := workRecordDetails(s, u, t, d)
		if err != nil {
			return nil, err
		}
		m := M{"id": uuid(), "tenantId": t, "number": str(d, "number"), "vendor": str(d, "vendor"), "vendorId": str(vendor, "id"), "vendorSnapshot": vendor, "description": str(d, "description"), "base": base, "formId": "work_order", "formVersion": number(f, "version"), "data": data, "createdBy": id, "createdAt": now()}
		for k, v := range contract {
			m[k] = v
		}
		for k, v := range details {
			m[k] = v
		}
		_, err = tx.Exec("INSERT INTO work_orders(id,tenant_id,number,base,form_id,form_version,created_by,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", str(m, "id"), t, str(m, "number"), base, "work_order", number(f, "version"), id, jsonBody(m))
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec("INSERT INTO work_order_vendors(order_id,tenant_id,vendor_id,snapshot) VALUES($1,$2,$3,$4)", str(m, "id"), t, str(vendor, "id"), jsonBody(vendor)); err != nil {
			return nil, err
		}
		if poID := str(details, "purchaseOrderId"); poID != "" {
			if _, err = tx.Exec("INSERT INTO work_order_purchase_reference(order_id,tenant_id,purchase_order_id) VALUES($1,$2,$3)", str(m, "id"), t, poID); err != nil {
				return nil, err
			}
		}
		if str(contract, "contractType") == "Material construction" {
			b := obj(d, "boq")
			raw, e := boqBytes(b)
			if e != nil {
				return nil, e
			}
			meta := obj(contract, "boq")
			if _, e = tx.Exec("INSERT INTO work_order_boq(order_id,tenant_id,name,mime,signed,content,sha256) VALUES($1,$2,$3,$4,true,$5,$6)", str(m, "id"), t, str(meta, "name"), str(meta, "mime"), raw, str(meta, "sha256")); e != nil {
				return nil, e
			}
		}
		return ok, logAudit(tx, u, t, "Work order draft created", str(m, "number"))
	case "transition":
		a := find(s["amendments"], str(d, "id"))
		if a == nil {
			return nil, errors.New("Amendment not found")
		}
		o := find(s["orders"], str(a, "orderId"))
		if str(o, "tenantId") != t {
			return nil, errors.New("Amendment not found in this project")
		}
		action := str(d, "action")
		if !can(s, u, t, action, str(o, "formId")) {
			return nil, deny()
		}
		st := status(s, a)
		stage := ""
		if action == "submit" {
			if st != "Draft" {
				return nil, errors.New("Only drafts can be submitted")
			}
			if str(a, "createdBy") != id && !admin(u) {
				return nil, deny()
			}
		} else {
			if str(a, "createdBy") == id && !admin(u) {
				return nil, errors.New("The creator cannot review, approve or reject their own amendment")
			}
			switch action {
			case "review":
				if st != "Review" {
					return nil, errors.New("Amendment is not awaiting review")
				}
			case "approve":
				var err error
				stage, err = financialApprovalStage(s, u, t, a, documentEvents(s["events"], "amendmentId", str(a, "id")), str(o, "formId"))
				if err != nil {
					return nil, err
				}
			case "reject":
				if st == "Draft" || st == "Approved" || st == "Rejected" {
					return nil, errors.New("Only pending submissions can be rejected")
				}
			default:
				return nil, errors.New("Unsupported action")
			}
		}
		m := M{"id": uuid(), "amendmentId": str(a, "id"), "action": action, "stage": stage, "actorId": id, "at": now(), "superuserApproval": admin(u) && action == "approve"}
		_, err := tx.Exec("INSERT INTO amendment_events(id,amendment_id,action,stage,actor_id,body) VALUES($1,$2,$3,$4,$5,$6)", str(m, "id"), str(a, "id"), action, stage, id, jsonBody(m))
		if err != nil {
			return nil, err
		}
		text := "Amendment " + action
		if action == "approve" {
			text = strings.ToUpper(stage[:1]) + stage[1:] + " approval recorded"
		}
		return ok, logAudit(tx, u, t, text, str(o, "number"))
	}
	if !admin(u) {
		return nil, errors.New("Only the platform superuser can manage configuration")
	}
	switch kind {
	case "rera":
		number := strings.ToUpper(strings.Join(strings.Fields(str(d, "number")), ""))
		if !regexp.MustCompile(`^[0-9]{6,34}$`).MatchString(number) {
			return nil, errors.New("Account number must contain 6–34 digits")
		}
		if strings.TrimSpace(str(d, "bank")) == "" || strings.TrimSpace(str(d, "ifsc")) == "" {
			return nil, errors.New("Bank and IFSC are required")
		}
		extra, version, extraErr := financialExtras(s, u, t, d, "rera_account", nil)
		if extraErr != nil {
			return nil, extraErr
		}
		m := M{"data": extra, "formVersion": version, "id": uuid(), "tenantId": t, "number": number, "bank": strings.TrimSpace(str(d, "bank")), "ifsc": strings.ToUpper(strings.TrimSpace(str(d, "ifsc"))), "label": str(d, "label")}
		_, err := tx.Exec("INSERT INTO rera_accounts(id,tenant_id,number,body) VALUES($1,$2,$3,$4)", str(m, "id"), t, number, jsonBody(m))
		if err != nil {
			return nil, err
		}
		return ok, logAudit(tx, u, t, "RERA bank account registered", number)
	case "membership":
		userID := str(d, "userId")
		if find(s["users"], userID) == nil {
			return nil, errors.New("User not found")
		}
		if admin(find(s["users"], userID)) {
			return nil, errors.New("The platform superuser does not need project roles")
		}
		roleIDs := arr(d, "roleIds")
		seen := map[string]bool{}
		for _, rid := range roleIDs {
			r, ok := rid.(string)
			if !ok || find(s["roles"], r) == nil || seen[r] {
				return nil, errors.New("Select valid distinct roles")
			}
			seen[r] = true
		}
		_, err := tx.Exec("INSERT INTO memberships(tenant_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING", t, userID)
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec("DELETE FROM membership_roles WHERE tenant_id=$1 AND user_id=$2", t, userID); err != nil {
			return nil, err
		}
		for r := range seen {
			if _, err = tx.Exec("INSERT INTO membership_roles(tenant_id,user_id,role_id) VALUES($1,$2,$3)", t, userID, r); err != nil {
				return nil, err
			}
		}
		return ok, logAudit(tx, u, t, "Project roles assigned", str(find(s["users"], userID), "name"))
	case "role-delete":
		role, assignments, err := roleRemoval(s, u, str(d, "id"))
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec("DELETE FROM membership_roles WHERE role_id=$1", str(role, "id")); err != nil {
			return nil, err
		}
		if _, err = tx.Exec("DELETE FROM roles WHERE id=$1", str(role, "id")); err != nil {
			return nil, err
		}
		projects := map[string]bool{t: true}
		for _, m := range assignments {
			projects[str(m, "tenantId")] = true
		}
		for project := range projects {
			if err = logVendorAudit(tx, u, project, "Global role deleted", str(role, "name"), M{"role": role, "assignments": assignments}, nil); err != nil {
				return nil, err
			}
		}
		return ok, nil
	case "role":
		if str(d, "id") != "" && find(s["roles"], str(d, "id")) == nil {
			return nil, errors.New("Role not found")
		}
		name := strings.TrimSpace(str(d, "name"))
		if name == "" {
			return nil, errors.New("Role name is required")
		}
		stage := str(d, "stage")
		if stage != "" && stage != "manager" && stage != "admin" && stage != "finance" {
			return nil, errors.New("Invalid approval stage")
		}
		perms := obj(d, "permissions")
		allowedActions := []any{"view", "create", "edit", "submit", "review", "approve", "reject", "amend"}
		for formID, raw := range perms {
			if latest(s, formID) == nil {
				return nil, errors.New("Permission form not found")
			}
			p, valid := raw.(map[string]any)
			if !valid {
				return nil, errors.New("Invalid permissions")
			}
			for _, a := range arr(p, "actions") {
				key, valid := a.(string)
				if !valid || !contains(allowedActions, key) {
					return nil, errors.New("Invalid permission action")
				}
			}
			for _, v := range obj(p, "fields") {
				if v != "hidden" && v != "view" && v != "edit" {
					return nil, errors.New("Invalid field access")
				}
			}
		}
		rid := str(d, "id")
		if rid == "" {
			rid = uuid()
		}
		m := M{"id": rid, "name": name, "stage": stage, "permissions": perms}
		_, err := tx.Exec("INSERT INTO roles(id,body) VALUES($1,$2) ON CONFLICT(id) DO UPDATE SET body=EXCLUDED.body", rid, jsonBody(m))
		if err != nil {
			return nil, err
		}
		return ok, logAudit(tx, u, t, "Global role permissions updated", name)
	case "form":
		name := strings.TrimSpace(str(d, "name"))
		if name == "" {
			return nil, errors.New("Form name is required")
		}
		fid := str(d, "id")
		if fid == "" {
			fid = uuid()
		}
		v := int64(1)
		if last := latest(s, fid); last != nil {
			v = number(last, "version") + 1
		}
		fields := append(arr(d, "fields"), arr(d, "itemFields")...)
		keys := map[string]bool{}
		keyPattern := regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
		reserved := map[string]bool{"amount": true, "description": true, "reason": true, "id": true, "base": true, "delta": true}
		if len(fields) > 50 {
			return nil, errors.New("Maximum 50 custom fields")
		}
		for _, raw := range fields {
			f, valid := raw.(map[string]any)
			key := str(f, "key")
			if !valid || !keyPattern.MatchString(key) || keys[key] || reserved[key] || strings.TrimSpace(str(f, "label")) == "" || !contains([]any{"text", "number", "date"}, str(f, "type")) {
				return nil, errors.New("Fields need unique lowercase keys, labels and supported types")
			}
			keys[key] = true
		}
		m := M{"id": fid, "name": name, "version": v, "fields": arr(d, "fields"), "itemFields": arr(d, "itemFields"), "createdBy": id, "createdAt": now()}
		_, err := tx.Exec("INSERT INTO form_versions(id,version,body) VALUES($1,$2,$3)", fid, v, jsonBody(m))
		if err != nil {
			return nil, err
		}
		return ok, logAudit(tx, u, t, "Financial form version published", name)
	}
	return nil, errors.New("Unknown operation")
}
func migrate() error {
	adminDB, err := sql.Open("postgres", os.Getenv("MIGRATION_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer adminDB.Close()
	tx, err := adminDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("SELECT pg_advisory_xact_lock(7249134)"); err != nil {
		return err
	}
	if _, err = tx.Exec("CREATE TABLE IF NOT EXISTS schema_migrations(version text PRIMARY KEY, checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(env("MIGRATIONS_DIR", "/app/migrations"), "*.sql"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return errors.New("No migration files found")
	}
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name := filepath.Base(path)
		checksum := hash(string(b))
		var previous string
		err = tx.QueryRow("SELECT checksum FROM schema_migrations WHERE version=$1", name).Scan(&previous)
		if err == nil {
			if previous != checksum {
				return fmt.Errorf("migration %s changed after application", name)
			}
			continue
		}
		if err != sql.ErrNoRows {
			return err
		}
		if _, err = tx.Exec(string(b)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err = tx.Exec("INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)", name, checksum); err != nil {
			return err
		}
		log.Printf("Applied %s", name)
	}
	password := os.Getenv("APP_DB_PASSWORD")
	if len(password) < 12 {
		return errors.New("APP_DB_PASSWORD must have at least 12 characters")
	}
	var exists bool
	if err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='ledger_app')").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec("CREATE ROLE ledger_app LOGIN PASSWORD " + pq.QuoteLiteral(password)); err != nil {
			return err
		}
	} else {
		if _, err = tx.Exec("ALTER ROLE ledger_app PASSWORD " + pq.QuoteLiteral(password)); err != nil {
			return err
		}
	}
	grants := `GRANT USAGE ON SCHEMA public TO ledger_app;
 GRANT SELECT,INSERT,UPDATE ON erp_extensions TO ledger_app;
 GRANT SELECT,INSERT ON erp_extension_events TO ledger_app;
 GRANT SELECT ON users,tenants,roles,memberships,membership_roles,form_versions,rera_accounts,work_orders,uploads,amendments,amendment_events,audit_log TO ledger_app;
 GRANT INSERT ON tenants,roles,memberships,membership_roles,form_versions,rera_accounts,work_orders,uploads,amendments,amendment_events,audit_log TO ledger_app;
 GRANT UPDATE ON roles,tenants TO ledger_app;
 GRANT SELECT,INSERT,UPDATE ON purchase_orders,finance_documents TO ledger_app;
 GRANT SELECT,INSERT ON vendor_invoices,finance_events,invoice_events,operational_events,work_order_events,work_order_boq TO ledger_app;
 GRANT SELECT,INSERT,UPDATE ON operational_records TO ledger_app;
 GRANT SELECT,INSERT ON purchase_events,stock_lots,stock_movements TO ledger_app;
 GRANT DELETE ON membership_roles,roles TO ledger_app;
 GRANT SELECT,INSERT,DELETE ON sessions TO ledger_app;
 GRANT SELECT,INSERT,UPDATE ON vendors TO ledger_app;
 GRANT SELECT,INSERT ON work_order_vendors,work_order_purchase_reference TO ledger_app;
 GRANT INSERT ON users TO ledger_app;
 GRANT UPDATE(password_hash) ON users TO ledger_app;`
	if _, err = tx.Exec(grants); err != nil {
		return err
	}
	return tx.Commit()
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func main() {
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := migrate(); err != nil {
			log.Fatal(err)
		}
		log.Print("Migrations complete")
		return
	}
	var err error
	db, err = sql.Open("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err = db.Ping(); err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", serveAPI)
	server := &http.Server{Addr: env("LISTEN_ADDR", ":8080"), Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("VRISE API listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}
