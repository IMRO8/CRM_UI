package main

import (
	"sort"
	"strings"
)

func latestProgress(s State, id, date string) int64 {
	var latest M
	for _, p := range suiteRecords(s, "progress", "") {
		if str(p, "orderId") == id && str(p, "date") <= date && (latest == nil || str(p, "date") > str(latest, "date") || (str(p, "date") == str(latest, "date") && str(p, "createdAt") > str(latest, "createdAt"))) {
			latest = p
		}
	}
	return number(latest, "percent")
}
func dashboardSummary(s State, u M) []M {
	out := []M{}
	for _, t := range s["tenants"] {
		id := str(t, "id")
		if t["deleted"] == true || !can(s, u, id, "view", "dashboard") {
			continue
		}
		var plan M
		for _, p := range suiteRecords(s, "project_plan", id) {
			plan = p
		}
		orders := []M{}
		var budget int64
		for _, o := range s["orders"] {
			if str(o, "tenantId") == id && orderStatus(s, o) == "Approved" {
				orders = append(orders, o)
				budget += effective(s, o)
			}
		}
		weighted := func(date string) int64 {
			var n int64
			for _, o := range orders {
				n += effective(s, o) * latestProgress(s, str(o, "id"), date)
			}
			if budget == 0 {
				return 0
			}
			return (n + budget/2) / budget
		}
		visible := func(k string) bool { return access(s, u, id, k, "dashboard") != "hidden" }
		m := M{"tenantId": id}
		if visible("progress") {
			m["progress"] = weighted("9999-12-31")
			rows := []M{}
			for _, o := range orders {
				rows = append(rows, M{"id": str(o, "id"), "number": str(o, "number"), "scope": str(o, "description"), "percent": latestProgress(s, str(o, "id"), "9999-12-31")})
			}
			months := []M{}
			for _, raw := range arr(plan, "monthlyTargets") {
				x := raw.(map[string]any)
				month := str(x, "month")
				var actual any = weighted(month + "-31")
				if month > now()[:7] {
					actual = nil
				}
				months = append(months, M{"month": month, "planned": number(x, "progress"), "actual": actual})
			}
			m["progressRows"] = rows
			m["progressMonths"] = months
		}
		if visible("cash") {
			receipts := suiteRecords(s, "collection", id)
			var collected int64
			months := map[string]bool{}
			for _, x := range receipts {
				collected += number(x, "amount")
				dt := str(x, "date")
				if len(dt) >= 7 {
					months[dt[:7]] = true
				}
			}
			for _, raw := range arr(plan, "monthlyTargets") {
				months[str(raw.(map[string]any), "month")] = true
			}
			keys := []string{}
			for k := range months {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			rows := []M{}
			for _, month := range keys {
				var target, actual int64
				for _, raw := range arr(plan, "monthlyTargets") {
					x := raw.(map[string]any)
					if str(x, "month") == month {
						target = number(x, "cash")
					}
				}
				for _, x := range receipts {
					if strings.HasPrefix(str(x, "date"), month) {
						actual += number(x, "amount")
					}
				}
				rows = append(rows, M{"month": month, "target": target, "actual": actual})
			}
			m["collected"] = collected
			m["cashMonths"] = rows
		}
		if visible("sales") {
			stages := M{"Lead": int64(0), "Reserved": int64(0), "Booked": int64(0), "Cancelled": int64(0)}
			var n int64
			for _, x := range suiteRecords(s, "sale", id) {
				stage := str(x, "status")
				stages[stage] = number(stages, stage) + 1
				if stage == "Booked" {
					n += number(x, "amount")
				}
			}
			m["sales"] = M{"stages": stages, "bookedValue": n, "target": number(plan, "salesTarget"), "unitTarget": number(plan, "unitTarget")}
		}
		if visible("budget") && canCost(s, u, id) {
			for _, c := range costSummary(s, u) {
				if str(c, "tenantId") == id {
					m["budget"] = M{"target": number(plan, "budget"), "workOrders": number(c, "workOrders"), "purchases": number(c, "purchases"), "additional": number(c, "additional"), "committed": number(c, "total"), "paid": number(c, "paid"), "tds": number(c, "tds"), "remaining": number(plan, "budget") - number(c, "total"), "categories": c["categories"]}
				}
			}
		}
		out = append(out, m)
	}
	return out
}
func vendorReports(s State, u M) []M {
	out := []M{}
	for _, v := range s["vendors"] {
		t, id := str(v, "tenantId"), str(v, "id")
		if v["deleted"] == true || !can(s, u, t, "view", "vendor_reporting") {
			continue
		}
		m := M{"tenantId": t, "vendorId": id, "name": str(v, "name"), "trade": str(v, "trade"), "status": str(v, "onboardingStatus")}
		var count, wo, po, quotes, selected, paid, tds, bal, feedback, totalRatings int64
		for _, o := range s["orders"] {
			if str(o, "vendorId") == id && str(o, "tenantId") == t {
				count++
				wo += effective(s, o)
			}
		}
		for _, p := range s["purchaseOrders"] {
			if str(p, "vendorId") == id && str(p, "tenantId") == t && poStatus(s, p) == "Approved" {
				po += number(p, "total")
			}
		}
		for _, q := range suiteRecords(s, "quotation", t) {
			if str(q, "vendorId") == id {
				quotes++
				for _, a := range suiteRecords(s, "award", t) {
					if str(a, "quotationId") == str(q, "id") && suiteStatus(s, a) == "Approved" {
						selected++
					}
				}
			}
		}
		for _, i := range s["invoices"] {
			if str(i, "vendorId") == id && str(i, "tenantId") == t {
				bal += invoiceBalance(s, i)
				for _, p := range s["payments"] {
					if str(p, "invoiceId") == str(i, "id") && financeStatus(s, p) == "Approved" {
						paid += number(p, "paid")
						tds += number(p, "tds")
					}
				}
			}
		}
		for _, f := range suiteRecords(s, "vendor_feedback", t) {
			if str(f, "vendorId") == id {
				feedback++
				totalRatings += number(f, "quality") + number(f, "timeliness") + number(f, "safety") + number(f, "communication")
			}
		}
		m["orders"] = count
		m["quotations"] = quotes
		m["selected"] = selected
		if access(s, u, t, "amount", "vendor_reporting") != "hidden" {
			m["workOrderValue"] = wo
			m["purchaseValue"] = po
			m["paid"] = paid
			m["tds"] = tds
			m["outstanding"] = bal
		}
		if access(s, u, t, "ratings", "vendor_reporting") != "hidden" {
			m["feedbackCount"] = feedback
			m["rating"] = nil
			if feedback > 0 {
				m["rating"] = float64(totalRatings) / float64(feedback*4)
			}
		}
		if access(s, u, t, "reference", "vendor_reporting") == "hidden" {
			for _, k := range []string{"vendorId", "name", "trade", "orders", "quotations", "selected"} {
				delete(m, k)
			}
		}
		out = append(out, m)
	}
	return out
}
