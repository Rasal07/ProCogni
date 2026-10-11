#!/usr/bin/env python3
"""End-to-end smoke test against a running proleaf-api and a real PostgreSQL.

`go test ./...` covers the same boundaries over in-memory stores, which is fast
and runs in CI. This covers the half that cannot: the actual SQL, the
transaction in bulk entry, the migrations, and the stats job writing a row that
the parent endpoint then reads.

    docker compose up -d db
    go run ./cmd/api -migrate-only
    go run ./cmd/api -bootstrap-admin +8613800000001
    go run ./cmd/api > server.log 2>&1 &
    python scripts/smoke.py --log server.log

The admin phone must already be bootstrapped. The dev SMS sender writes codes
to the log, which is where this reads them from; it will not work against an
environment using a real gateway, and that is intentional.
"""
import argparse
import json
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

UUID_RE = re.compile(r"[0-9a-fA-F]{8}-(?:[0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}")
CODE_RE = re.compile(r"code:\s*(\d{6})")

failures = []
args = None


def check(label, ok, detail=""):
    print(f"  [{'PASS' if ok else 'FAIL'}] {label}" + (f"  -- {detail}" if detail and not ok else ""))
    if not ok:
        failures.append(label)


def call(method, path, token=None, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(args.base + "/v1" + path, data=data, method=method)
    if data:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            raw = r.read().decode()
            return r.status, (json.loads(raw) if raw else {}), raw
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        return e.code, (json.loads(raw) if raw else {}), raw


def sign_in(phone):
    """Request an OTP, read it back out of the dev sender's log, and verify."""
    status, _, raw = call("POST", "/auth/otp/request", body={"phone": phone})
    if status == 429:
        # The resend interval is deliberately long enough to be felt.
        print(f"  ...waiting out the OTP resend interval for {phone}")
        time.sleep(61)
        status, _, raw = call("POST", "/auth/otp/request", body={"phone": phone})
    assert status == 202, (status, raw)

    code = None
    for _ in range(40):
        with open(args.log, encoding="utf-8", errors="replace") as fh:
            for line in reversed(fh.readlines()):
                if '"sms (dev sender' in line and phone[-11:] in line:
                    m = CODE_RE.search(line)
                    if m:
                        code = m.group(1)
                        break
        if code:
            break
        time.sleep(0.25)
    assert code, f"no OTP code in {args.log} for {phone}"

    status, body, raw = call("POST", "/auth/otp/verify", body={"phone": phone, "code": code})
    assert status == 200, (status, raw)
    return body["token"], body["role"]


def main():
    global args
    p = argparse.ArgumentParser()
    p.add_argument("--base", default="http://127.0.0.1:8080")
    p.add_argument("--log", required=True, help="the API server's log file")
    p.add_argument("--policy-version", default="2026-01-01")
    p.add_argument("--admin-phone", default="+8613800000001")
    p.add_argument("--teacher-phone", default="+8613800000002")
    p.add_argument("--parent-phone", default="+8613800000003")
    p.add_argument("--skip-statsjob", action="store_true")
    args = p.parse_args()

    print("\n== sign-in ==")
    admin_token, role = sign_in(args.admin_phone)
    check("the bootstrapped admin signs in as admin", role == "admin", role)

    print("\n== admin provisioning ==")
    status, cls, raw = call("POST", "/admin/classes", admin_token,
                            {"name": "CAT4 Blue", "cat_stage": 4, "academic_year": "2026"})
    check("create class", status == 201, raw)
    class_id = cls.get("id")

    status, teacher, raw = call("POST", "/admin/accounts", admin_token,
                                {"phone": args.teacher_phone, "role": "teacher", "display_name": "Ms Wu"})
    check("create teacher account", status == 201, raw)

    status, _, raw = call("POST", f"/admin/classes/{class_id}/teachers", admin_token,
                          {"account_id": teacher["id"]})
    check("assign teacher to class", status == 201, raw)

    students = []
    for name in ["Ana", "Bai", "Cui", "Du", "Er", "Fan"]:
        status, st, raw = call("POST", "/admin/students", admin_token,
                               {"display_name": name, "cat_stage": 4,
                                "class_id": class_id, "date_of_birth": "2016-05-01"})
        assert status == 201, (status, raw)
        students.append(st)
    check("create six students", len(students) == 6)
    check("the student response omits date_of_birth", "2016" not in json.dumps(students[0]))

    status, _, raw = call("POST", "/admin/students", admin_token,
                          {"display_name": "Wrong stage", "cat_stage": 5,
                           "class_id": class_id, "date_of_birth": "2016-05-01"})
    check("a student whose stage differs from the class is rejected", status == 400, raw)

    status, issued, raw = call("POST", "/admin/enrollment-codes", admin_token,
                               {"student_id": students[0]["id"]})
    check("issue an enrollment code", status == 201, raw)
    code = issued.get("code")
    check("the code comes back in display form",
          bool(re.fullmatch(r"[0-9A-Z]{4}-[0-9A-Z]{4}-[0-9A-Z]{4}", code or "")), code)

    print("\n== teacher bulk entry ==")
    teacher_token, role = sign_in(args.teacher_phone)
    check("the provisioned teacher signs in as teacher", role == "teacher", role)

    status, classes, raw = call("GET", "/teacher/classes", teacher_token)
    check("teacher lists their classes", status == 200 and len(classes["classes"]) >= 1, raw)

    status, roster, raw = call("GET", f"/teacher/classes/{class_id}/students", teacher_token)
    check("teacher lists the roster", status == 200 and len(roster["students"]) == 6, raw)

    today = time.strftime("%Y-%m-%d")
    entries = [{"student_id": s["id"], "score": 60 + i * 6, "participation": 3,
                "note": "internal note"} for i, s in enumerate(students)]
    status, result, raw = call("POST", "/teacher/assessments/bulk", teacher_token,
                               {"session_date": today, "entries": entries})
    check("bulk entry writes the whole batch", status == 201 and result["recorded"] == 6, raw)

    bad = entries + [{"student_id": "3f9a5c21-77bd-4d0e-9c14-0b6e9a2d5f33", "score": 50}]
    status, body, raw = call("POST", "/teacher/assessments/bulk", teacher_token,
                             {"session_date": today, "entries": bad})
    check("a batch naming a student outside the class is rejected whole", status == 404, raw)
    check("the failing index is reported",
          body.get("error", {}).get("details", {}).get("index") == 6, raw)

    status, hist, raw = call("GET", f"/teacher/students/{students[0]['id']}/history", teacher_token)
    check("teacher reads history", status == 200, raw)
    check("the rejected batch wrote nothing at all", len(hist["sessions"]) == 1, raw)
    check("teacher sees their own note", "internal note" in raw)

    status, _, raw = call("POST", "/teacher/assessments", teacher_token,
                          {"student_id": students[0]["id"], "score": 140})
    check("an out-of-range score is rejected", status == 400, raw)
    status, _, raw = call("POST", "/teacher/assessments", teacher_token,
                          {"student_id": students[0]["id"], "score": 70, "session_date": "2099-01-01"})
    check("a future session_date is rejected", status == 400, raw)

    if not args.skip_statsjob:
        print("\n== stats job ==")
        subprocess.run(["go", "run", "./cmd/statsjob"], check=True, capture_output=True)
        print("  [PASS] statsjob ran")

    print("\n== parent bind and read ==")
    parent_token, role = sign_in(args.parent_phone)
    check("an unknown phone becomes a parent", role == "parent", role)

    status, _, raw = call("POST", "/parent/bind", parent_token,
                          {"enrollment_code": code, "policy_version": "not-the-current-one",
                           "accepted_scopes": ["progress", "comparison"]})
    check("bind refuses a superseded policy version", status == 409, raw)

    status, _, raw = call("POST", "/parent/bind", parent_token,
                          {"enrollment_code": code, "policy_version": args.policy_version,
                           "accepted_scopes": ["progress", "comparison"]})
    check("bind with a valid code", status == 201, raw)

    status, _, raw = call("POST", "/parent/bind", parent_token,
                          {"enrollment_code": code, "policy_version": args.policy_version,
                           "accepted_scopes": ["progress", "comparison"]})
    check("the code is single use", status == 404, raw)

    child = students[0]["id"]
    status, kids, raw = call("GET", "/parent/children", parent_token)
    check("parent lists exactly their own child", status == 200 and len(kids["children"]) == 1, raw)

    status, prog, raw = call("GET", f"/parent/children/{child}/progress", parent_token)
    check("parent reads progress", status == 200 and len(prog["sessions"]) == 1, raw)
    check("progress omits the teacher note", "internal note" not in raw, raw)

    status, comp, raw = call("GET", f"/parent/children/{child}/comparison", parent_token)
    check("parent reads a comparison", status == 200, raw)
    check("own score is the child's own mean for the month", comp.get("own_score") == 60, raw)
    check("aggregates are published for a cohort of six",
          bool(comp.get("class")) and comp["class"]["student_count"] == 6, raw)
    check("the class mean is right", comp["class"]["mean_score"] == 75, raw)
    check("the comparison contains no UUID", not UUID_RE.search(raw), raw)
    check("the comparison contains no student name", "Ana" not in raw, raw)

    print("\n== negative authorization (skeleton guide, section 4) ==")
    status, _, raw = call("GET", "/teacher/classes", parent_token)
    check("1. parent token on a teacher route -> 403", status == 403, f"{status} {raw}")

    status, _, raw = call("GET", f"/parent/children/{students[1]['id']}/progress", parent_token)
    check("2. parent requesting an unbound student -> 404", status == 404, f"{status} {raw}")

    status, _, raw = call("GET", "/parent/children/11111111-1111-1111-1111-111111111111/progress", parent_token)
    check("   an id belonging to nobody -> 404", status == 404, f"{status} {raw}")

    status, _, raw = call("GET", "/parent/children", None)
    check("   no token -> 401", status == 401, f"{status} {raw}")

    # 3 (revoked binding) is further down, once the admin has revoked it.

    status, _, raw = call("PUT", f"/parent/children/{child}/consent", parent_token,
                          {"scope": "comparison", "granted": False,
                           "policy_version": args.policy_version})
    check("consent can be withdrawn", status == 200, raw)
    status, _, raw = call("GET", f"/parent/children/{child}/comparison", parent_token)
    check("   withdrawn consent blocks the comparison -> 403", status == 403, f"{status} {raw}")
    check("   and names the reason", "consent_required" in raw, raw)
    status, _, raw = call("GET", f"/parent/children/{child}/progress", parent_token)
    check("   the other scope is unaffected", status == 200, f"{status} {raw}")

    print("\n== admin management ==")
    nobody = "11111111-1111-1111-1111-111111111111"

    def admin(method, path, body=None):
        return call(method, path, admin_token, body)

    status, school, raw = admin("POST", "/admin/schools", {"name": "Pudong Experimental"})
    check("create a school", status == 201, raw)
    status, _, raw = admin("PATCH", f"/admin/schools/{school['id']}", {"name": "Pudong Experimental (East)"})
    check("rename a school", status == 200, raw)
    status, body, raw = admin("GET", "/admin/schools")
    check("list schools", status == 200 and "Pudong Experimental (East)" in raw, raw)

    status, body, raw = admin("PATCH", f"/admin/classes/{class_id}", {"school_id": school["id"]})
    check("file a class under a school, leaving its other fields alone",
          status == 200 and body.get("school_id") == school["id"]
          and body.get("name") == "CAT4 Blue" and body.get("student_count") == 6, raw)
    status, _, raw = admin("PATCH", f"/admin/classes/{class_id}", {"school_id": nobody})
    check("a class cannot be filed under a school that does not exist -> 404", status == 404, f"{status} {raw}")
    status, body, raw = admin("GET", f"/admin/classes?school_id={school['id']}")
    check("list classes by school", status == 200 and len(body["classes"]) == 1, raw)
    status, body, raw = admin("GET", f"/admin/classes/{class_id}")
    check("a class lists its teachers", status == 200 and len(body["teachers"]) == 1, raw)
    status, _, raw = admin("POST", f"/admin/classes/{nobody}/teachers", {"account_id": teacher["id"]})
    check("assigning a teacher to a class that does not exist -> 404", status == 404, f"{status} {raw}")

    status, other, raw = admin("POST", "/admin/classes",
                               {"name": "CAT5 Red", "cat_stage": 5, "academic_year": "2026",
                                "school_id": school["id"]})
    check("create a second class", status == 201, raw)
    status, _, raw = admin("GET", f"/admin/students?class_id={other['id']}")
    check("an empty roster is [] and not null", status == 200 and '"students":[]' in raw, raw)

    mover = students[5]["id"]
    status, body, raw = admin("PATCH", f"/admin/students/{mover}", {"class_id": other["id"]})
    check("moving a student takes the new class's stage", status == 200 and body.get("cat_stage") == 5, raw)
    status, body, raw = admin("PATCH", f"/admin/students/{mover}",
                              {"class_id": class_id, "display_name": "Fan Li"})
    check("and back, renamed", status == 200 and body.get("cat_stage") == 4
          and body.get("display_name") == "Fan Li", raw)

    status, _, raw = admin("POST", f"/admin/students/{mover}/archive")
    check("archive a student", status == 200 and "archived_at" in raw, raw)
    status, body, raw = admin("GET", f"/admin/students?class_id={class_id}")
    check("   gone from the admin roster", status == 200 and len(body["students"]) == 5, raw)
    status, body, raw = call("GET", f"/teacher/classes/{class_id}/students", teacher_token)
    check("   gone from the teacher's roster", status == 200 and len(body["students"]) == 5, raw)
    status, body, raw = admin("GET", f"/admin/students?class_id={class_id}&include_archived=true")
    check("   still there with include_archived", status == 200 and len(body["students"]) == 6, raw)
    status, _, raw = admin("POST", f"/admin/students/{mover}/restore")
    check("restore a student", status == 200 and "archived_at" not in raw, raw)

    status, body, raw = admin("GET", "/admin/accounts?role=teacher")
    check("list accounts by role", status == 200 and len(body["accounts"]) == 1, raw)
    status, body, raw = admin("GET", "/admin/accounts?phone=" + urllib.parse.quote(args.teacher_phone))
    check("find an account by phone", status == 200 and len(body["accounts"]) == 1, raw)
    status, body, raw = admin("PATCH", f"/admin/accounts/{teacher['id']}", {"display_name": "Ms Wu Lan"})
    check("rename an account", status == 200 and body.get("display_name") == "Ms Wu Lan", raw)
    status, body, raw = admin("POST", f"/admin/accounts/{teacher['id']}/disable")
    check("disable an account", status == 200 and body.get("disabled_at"), raw)
    status, _, raw = call("GET", "/teacher/classes", teacher_token)
    check("   its unexpired token stops working at once -> 403", status == 403, f"{status} {raw}")
    status, _, raw = admin("POST", f"/admin/accounts/{teacher['id']}/enable")
    check("enable it again", status == 200 and "disabled_at" not in raw, raw)
    status, _, raw = call("GET", "/teacher/classes", teacher_token)
    check("   and the token works again", status == 200, f"{status} {raw}")
    status, me, raw = admin("GET", "/admin/accounts?role=admin")
    status, _, raw = admin("POST", f"/admin/accounts/{me['accounts'][0]['id']}/disable")
    check("an admin cannot disable themself -> 400", status == 400, f"{status} {raw}")

    print("\n== enrollment code lifecycle ==")
    status, sheet, raw = admin("POST", f"/admin/classes/{class_id}/enrollment-codes")
    check("the class sheet issues a named code per student",
          status == 201 and len(sheet["codes"]) == 6 and all(c["display_name"] for c in sheet["codes"]), raw)
    spare = next(c for c in sheet["codes"] if c["student_id"] == students[1]["id"])
    status, _, raw = admin("POST", f"/admin/enrollment-codes/{spare['id']}/revoke")
    check("revoke an unused code", status == 200, raw)
    status, _, raw = admin("POST", f"/admin/enrollment-codes/{spare['id']}/revoke")
    check("   revoking it twice -> 409", status == 409, f"{status} {raw}")
    status, _, raw = admin("POST", f"/admin/enrollment-codes/{nobody}/revoke")
    check("   revoking a code that does not exist -> 404", status == 404, f"{status} {raw}")
    status, _, raw = call("POST", "/parent/bind", parent_token,
                          {"enrollment_code": spare["code"], "policy_version": args.policy_version,
                           "accepted_scopes": ["progress"]})
    check("   a revoked code does not bind -> 404", status == 404 and "code_not_usable" in raw, f"{status} {raw}")
    status, body, raw = admin("GET", f"/admin/enrollment-codes?student_id={child}")
    check("codes list with their status",
          status == 200 and sorted(c["status"] for c in body["codes"]) == ["active", "consumed"], raw)
    status, body, raw = admin("GET", f"/admin/enrollment-codes?class_id={class_id}")
    check("codes list by class, without any plaintext",
          status == 200 and len(body["codes"]) == 7 and '"code"' not in raw, raw)
    used = next(c for c in body["codes"] if c["status"] == "consumed")
    status, _, raw = admin("POST", f"/admin/enrollment-codes/{used['id']}/revoke")
    check("   a consumed code is too late to revoke -> 409", status == 409, f"{status} {raw}")

    print("\n== binding revocation and the full-marks fix ==")
    status, body, raw = admin("GET", f"/admin/students/{child}")
    check("a student lists their guardians, and no date of birth",
          status == 200 and len(body["guardians"]) == 1 and "2016" not in raw, raw)
    guardian = body["guardians"][0]["account_id"]
    status, _, raw = admin("DELETE", f"/admin/students/{child}/guardians/{guardian}")
    check("3. revoke a binding", status == 204, f"{status} {raw}")
    status, _, raw = call("GET", f"/parent/children/{child}/progress", parent_token)
    check("   the revoked parent -> 404", status == 404, f"{status} {raw}")
    status, body, raw = call("GET", "/parent/children", parent_token)
    check("   and the child is gone from their list", status == 200 and body["children"] == [], raw)

    status, _, raw = call("POST", "/teacher/assessments", teacher_token,
                          {"student_id": students[2]["id"], "score": 100})
    check("a score of exactly 100 can be stored", status == 201, f"{status} {raw}")

    status, _, raw = admin("DELETE", f"/admin/classes/{class_id}/teachers/{teacher['id']}")
    check("unassign a teacher", status == 204, f"{status} {raw}")
    status, body, raw = call("GET", "/teacher/classes", teacher_token)
    check("   who then has no classes", status == 200 and body["classes"] == [], raw)
    status, _, raw = call("GET", f"/teacher/classes/{class_id}/students", teacher_token)
    check("   and cannot read the roster -> 404", status == 404, f"{status} {raw}")

    status, _, raw = call("GET", "/nope", admin_token)
    check("an unknown route uses the error envelope",
          status == 404 and '"code":"not_found"' in raw and "request_id" in raw, f"{status} {raw}")

    print("\n== audit ==")
    status, applog, raw = call("GET", "/admin/audit?stream=app&limit=200", admin_token)
    check("admin reads the app stream", status == 200, raw)
    for want in ["record_assessment", "bind_guardian", "view_progress",
                 "view_comparison", "list_classes"]:
        check(f"the app stream contains {want}", want in json.dumps(applog))

    status, adminlog, raw = call("GET", "/admin/audit?stream=admin&limit=200", admin_token)
    check("admin actions land in their own stream",
          status == 200 and "issue_enrollment_code" in json.dumps(adminlog), raw)
    check("app-stream rows do not appear in the admin stream",
          "record_assessment" not in json.dumps(adminlog), raw)

    print("\n" + ("ALL CHECKS PASSED" if not failures else f"{len(failures)} FAILURES: {failures}"))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
