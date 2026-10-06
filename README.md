# VRISE — construction ERP sample

Vue 3 + Vuetify 3 frontend, Go HTTP API, PostgreSQL 16, and Docker Compose with automatic versioned migrations.

This is a reviewable sample, not a production ERP release. The hosted sample runs on in-memory data and resets on refresh. The Docker build disables the sample adapter and uses the authenticated Go API and persistent PostgreSQL database.

## Start the full stack

```bash
cp .env.example .env
# Replace the two database passwords in .env with long, URL-safe alphanumeric values.
docker compose up --build -d
```

Open http://localhost:8088. First startup waits for PostgreSQL health, runs the one-shot `migrate` service, then starts the Go API and web frontend. The API uses a separate `ledger_app` database role; the migration owner is not the application role. A named volume retains database data.

All four sample accounts use `BuildLedger-demo-2026!` as the login password:

| Account | User ID | Access |
| --- | --- | --- |
| Rohith Krishna | u1 | Platform superuser across all projects |
| Ananya Rao | u2 | Project manager in Skyline and Northgate |
| Kiran Shah | u3 | Finance approver in Skyline and Northgate |
| Ravi Kumar | u4 | Site engineer in Skyline; engineer + manager in Northgate |

```bash
docker compose ps
docker compose logs migrate api
docker compose down
```

`docker compose down` retains data. Restart migrations after adding a new migration with `docker compose run --rm migrate`, followed by `docker compose up -d`. Never edit an already-applied migration: checksum validation rejects that. When rebuilding all services after source changes, use `docker compose up --build -d --force-recreate` so the one-shot service reruns. Migrations acquire a database advisory lock and apply atomically.

## Confirmed business model

- Each project is a tenant. Global identities can have multiple project memberships and multiple roles in the same project.
- Roles are shared definitions across every project. The superuser configures actions and hidden/view/edit field permissions. Project assignments determine which roles apply.
- RERA accounts mean bank accounts, not project registration numbers. Account numbers are stored as text, preserving leading zeros, and are globally unique across all projects. Bank, IFSC and purpose are captured. The sample's account-number length constraint is an application choice, not a regulatory statement. It does not verify bank ownership or IFSC against a bank directory.
- Financial changes are signed deltas in INR including tax, stored in integer paise. Original ₹12 lakh + approved ₹2 lakh = ₹14 lakh. An approved negative ₹2 lakh amendment gives ₹10 lakh.
- Superuser submissions: Draft → Submit → Superuser approval, with no finance or separate reviewer required. Regular submissions: Draft → Submit → Review → Admin + Superuser or Finance + Superuser. Either approval order is allowed; the superuser is mandatory and the two approvers must be different people. Regular creators cannot review, approve or reject their own submissions. Admin is a separate configurable role; Project Manager alone can review but cannot fill the Admin approval requirement.
- Pending and rejected changes are excluded from committed totals. Financial values are changed through new amendments, never overwritten. The record viewer shows original values, deltas, statuses, users and approval events.
- CSV uploads require every row to be valid. Validation checks work order identity within the current project, permissions, signed decimal amounts, reasons, sequential projected totals and file limits. Imported rows become draft amendments. The original accepted CSV and its checksum are retained. The commit revalidates the CSV; every approval rechecks the effective total under a work-order row lock.
- Rejected CSV validation previews do not create financial records or upload records. Retaining rejected files is a separate future policy.
- Global role changes and membership changes are audited. Financial form updates publish a new immutable version. Existing work orders retain the schema version used when they were created.

## Try the sample

1. Open WO-SKY-001: original ₹12 lakh + approved ₹2 lakh = ₹14 lakh.
2. As Rohith (superuser), open Approval inbox and directly approve the pending electrical change.
3. Directly approve the plumbing reduction as Rohith; no finance approval is needed for these superuser-created submissions. To test a regular pair, assign the separate Admin role to Ananya, create and submit an amendment as Kiran, review and approve as Ananya, then approve as Rohith.
4. Switch to Ravi in Skyline. Amounts and RERA bank accounts are restricted. Switch to Northgate to see his additional manager role.
5. As superuser, assign several roles to a user, edit a shared role, or add a field to the Work Order template. New work orders use the new version; old ones retain their original version.
6. Download the CSV template. Try a negative amendment exceeding a work order's current total; the entire upload is blocked.
7. Try registering an existing bank account in another project; global uniqueness rejects it.

## CSV contract

```csv
work_order,delta_including_tax,reason
WO-SKY-001,200000,Additional foundation scope
WO-SKY-002,-50000,Reduced cable scope
```

Amounts are rupees, not paise. No currency symbols, thousand separators or scientific notation. Maximum two decimal places, 500 rows and 1 MB. Numbers in API commands are integer paise. UI totals include supplied tax; the sample does not independently calculate GST, tax or retention deductions.

## What is implemented and what needs a production decision

Implemented: project tenants, existing-user role assignment, global bank-account uniqueness, sessions, server authorization, field redaction, PostgreSQL row-level tenant isolation, fixed work-order core fields plus dynamic additional fields, immutable template versions, work orders, signed amendments, approvals, CSV staging/atomic import, original-upload retention, and append-only financial/audit records.

The dynamic record workflow currently applies to Work Order, Purchase Order, Expense and Vendor Payment. Administrators can define other financial templates and configure their permissions, but record screens and calculation rules for those custom document types are not yet connected. Core RERA bank fields are fixed in this sample. Its form definition is available for permission mapping; extra bank template fields are not yet connected to bank record entry.

Legacy work orders retain their starting committed values. New work orders require an approved selected quotation and their own approval workflow before they contribute to commitments. Work order amendments do not support in-place editing; create a new draft and submit the intended amendment. Purchase order drafts can be edited before submission. The `edit` permission is available for field configuration, while changing a committed financial value requires `amend`.

Before a production rollout, settle the first financial document types and fields, approval amount thresholds/delegates, draft replacement policy, original-work-order approval, original CSV versus revised upload matching, tax rules, user onboarding, password management/SSO, retention, backups, and whether a work order must explicitly reference a RERA bank account. The superuser can create regular users, generate or regenerate credentials, and assign project roles. Regenerating credentials revokes active sessions in the Go backend. Passwords are displayed once, stored as bcrypt hashes by PostgreSQL, and excluded from state and audit payloads. Account recovery/SSO is not part of this sample. Sample credentials must not be exposed on a public production installation. Use HTTPS and `COOKIE_SECURE=true` behind a correctly configured reverse proxy when deploying outside a local machine.

## Verification

```bash
npm ci
npm test
npm run build
cd backend
go test ./...
go build .
```

Node tests check financial transitions, signed-decimal handling, CSV validation, and execute actual PostgreSQL schema/seed/RLS/trigger rules through PGlite. Go tests check monetary conversion, server field redaction, multiple project roles, CSV errors and approved totals. A Vue/Vuetify server-render check verifies that the dashboard is populated and components are registered. Docker was not available in the authoring environment, so the Compose stack has not been exercised end to end there. Native PostgreSQL 16 integration and browser interaction testing remain to run on the target installation.

## Layout

- `src/App.vue`: application views and Vuetify dialogs.
- `src/domain.js`: financial rules and illustrative data.
- `src/client.js`: in-memory sample adapter or real HTTP API adapter.
- `backend/main.go`: authenticated API, permissions, commands, CSV parser and migration runner.
- `migrations/001_schema.sql`: schema, uniqueness constraints, append-only guards, approval invariants and row-level tenant policies.
- `migrations/002_demo_seed.sql`: sample data; this is a demo seed, not a production onboarding migration.
- `compose.yaml`, Dockerfiles and `nginx.conf`: runnable stack.

The original VRISE repository was not accessed or modified. This is a separate sample for reviewing the proposed ERP behavior.

## Vendors, users and superuser approvals

- The Vendors tab contains separate records for each project. Vendor names can repeat within or across projects. Details include name, construction trade, contact person, phone, email, optional GSTIN and address. Only the platform superuser manages active directory profiles; authorized onboarding users can prepare their own intake drafts and submit them for superuser activation.
- New work orders select an active vendor from the same project. The brief profile is shown in work-order details. Current directory edits appear in the live profile, while the issuance snapshot is retained. Deleting removes the vendor from the directory and selection options; existing work orders keep their original details. Vendor profile edits/deletions are audited.
- Vendor form access and individual fields are configured alongside the existing role permissions. Server responses redact hidden vendor fields and enforce project boundaries. Work-order/vendor references also use compound database foreign keys to prevent linking a vendor from another project.
- In Users & roles, Add user creates a username and a cryptographically generated 48-character password. Roles can be selected independently for multiple projects. A project with no assigned roles gives no project access. Username uniqueness is case-insensitive. No new account can be created as a platform superuser through this flow.
- Generated credentials are returned once to the administrator. Generate credentials on an existing regular user replaces their password and signs out existing backend sessions. Share them manually through an appropriate channel. No password is included in audit logs or user listings.
- The original pending amendments were created by the superuser; they now offer direct Superuser approval, without requiring finance approval.
- Migration `003_vendors_users_superuser.sql` upgrades existing databases without modifying already-applied migrations or rewriting financial records. It adds vendors, preserved vendor references, unique usernames, user-management policies, and the superuser creator-exclusion exception.

The hosted preview still uses temporary sample data. New accounts and generated credentials there work until refresh; the Docker version stores accounts and vendor profiles in PostgreSQL.

## Projects, purchase orders and carry-over inventory

- Projects is a dedicated superuser-only management page with add, edit and delete controls. Project codes remain unique, including after deletion. Deleting a project removes it from active selection and regular-user access, retaining financial, RERA and audit history. Transfer or issue remaining owned inventory before deletion. Pending documents on a deleted project remain historical and cannot progress.
- Purchase order drafts select a project vendor and contain purchased or rented material/equipment lines. Rates include tax; quantities support three decimals and currency two decimals. All calculations are stored in integer paise and integer thousandths of the stated unit. Rental quantity or rate includes the hire period; there is no automated rental billing schedule.
- Workflow: superuser-created POs require submission and one explicit superuser approval. Regular POs require review and two different approvers: Superuser + Admin or Superuser + Finance. Admin and Finance together cannot finalize an order without the superuser. Regular creators may only submit their own drafts. Drafts can be edited; submitted, approved and rejected records are retained without overwriting. Custom extra Purchase Order fields use immutable template versions.
- Project committed cost is work-order effective values plus the full totals of approved purchase orders, including rentals. PO line rates must represent additional project costs; the sample does not reconcile duplicate scope between a work order and a purchase order. Pending and rejected POs contribute zero.
- Approved purchased lines enter owned inventory through explicit partial or full receipts, with cumulative receipts capped at the ordered quantity. Rental lines never create inventory. Stock is tracked as lots tied to the original PO line, material and unit. Inventory contains no duplicated purchase-cost entries.
- Unused stock can transfer between projects where the user has inventory editing/receiving permissions. A transfer records equal outgoing and incoming quantities with a shared reference; the original purchase cost stays with its purchasing project. Stock used on site is recorded with Issue for use. Negative stock and excess receipts are blocked by the API and PostgreSQL guards. Stock movements are immutable. Unit conversion, returns, matching invoices to PO receipts and warehouse sublocations are outside this sample.
- Purchase Order and Inventory actions and fields are configurable through Users & roles. Stock receipts use Inventory create permission; issues and outgoing transfers use edit. Both material and quantity fields must be editable for stock commands. Purchase Order amount, description and materials fields must be editable to create/edit a draft. Managers and finance users receive these permissions in migration 004; engineers start with view-only access and hidden purchase amounts.
- Migration `004_projects_procurement_inventory.sql` upgrades project management and adds purchase-order approvals, receipts and balanced inventory transfer ledgers without changing earlier migrations. The Go API locks both transfer projects in sorted order and serializes permission changes before financial commands.

To try direct approval, open Purchase orders as Rohith, submit PO-SKY-001 and choose Superuser approval. Receive the TMT steel from its purchased line. In Inventory, transfer some unused steel to Northgate and switch projects to see the carried-over stock. The concrete mixer rental increases committed cost but has no receipt action and never appears in owned inventory.


## Edit and delete roles

In Users & roles, each shared role has visible Edit and Delete buttons available only to the platform superuser. Edit updates its name, approval stage and form/field permissions across all projects. Delete asks for confirmation and shows the number of user-project assignments affected, then removes the role and all its assignments atomically. Other roles, accounts, financial records and prior approvals are retained; an audit entry preserves the deleted role definition and affected assignments on each affected project. A user with no remaining project roles loses project access. Stale edits cannot recreate a deleted role.

Each regular user also has Edit roles (or Assign roles) and Remove roles controls for the currently selected project. Remove roles revokes that project's assignments while retaining the account and other project memberships. Superuser access is independent of assignable roles and cannot be removed through these controls. Migration 005 adds the superuser-only role deletion policy; migrations continue to run through Docker Compose.

## Vendor placement and document fields

- A full vendor profile appears above the work-order list with a project-vendor selector, above the work-order entry fields, and above its item details. Vendor edits remain superuser-only and hidden vendor fields remain redacted.
- The purchase-order register shows ordered materials, supplier, approved by, approved date and value. Approved by lists actual approval actors and their recorded stages; approved date is the last required approval date and stays pending until the applicable approval rule is complete. Superuser-created POs show the direct superuser approval date. Completed records from the previous manager/finance policy retain their original approvals and dates. These are read from immutable workflow events. Each PO now captures its document date separately from approval dates. Existing records that did not capture a document date use their original creation date without rewriting their financial history.
- Work orders can contain multiple items with description, unit, start/end period dates, quantity, rate and calculated amount. The original work-order value is the sum of item amounts, rounded to paise per line. Quantities support three decimal places. Period dates are descriptive and do not multiply the amount. Signed amendments continue to change the current effective total while the original item values remain retained.
- A work order can optionally select an approved PO from the same project, filling in its canonical PO number and document date. This is a reference, not a second charge for that PO: the work-order scope/rates must be separate additional costs. Work-order and approved PO totals still contribute independently to project cost. The API and compound database foreign keys enforce project boundaries and validate snapshots.
- Earlier work orders retain their original description and amount; missing quantities, units and dates are shown as not recorded. The original records are not fabricated or overwritten to fill these new fields. The Work Order role editor includes quantity, unit, period and purchase reference permissions; rates and item amounts follow financial amount permissions. The Purchase Order editor includes document-date permissions. Manager/finance defaults allow editing the new fields; engineer defaults allow viewing dates and quantities while restricting financial amounts.
- Migration 006 adds the PO document date, immutable WO/PO reference links and exact item-value/date validation. Docker Compose continues to apply all pending migrations before the API starts.

## VRISE brand theme

The uploaded THE LOFT logo has been adapted into the VRISE wordmark at `public/vrise-logo.png`. `src/theme.js` is the palette source for both Vuetify and CSS variables: copper/gold/bronze primary colors, ivory/champagne/linen neutrals, espresso/charcoal/umber text and navigation, and blush/rose/stone accents. Copper buttons use espresso text for readable contrast. All financial and administrative screens share this theme. Existing seeded login credentials remain unchanged.

## Updated approval routing

Migration `007_superuser_approval_policy.sql` adds a separate configurable Admin role and enforces the new approval combinations in PostgreSQL as well as Go and the hosted sample. Assign Admin through Users & roles; it is not automatically granted to existing managers. Admin/Finance approval requires the approval action and stage on the same assigned role in that project. Superusers cannot fill the partner slot of a regular submission.

Creator superuser status is captured as immutable document metadata. Completed and rejected documents retain policy 1; pending and new documents use policy 2. Earlier manager events on pending records remain visible as previous-policy history, while the new pair is required to finalize them. Existing values, dates and event bodies are retained. The migration only backfills routing metadata under transactional table locks; immutability guards are restored before commit. New database triggers derive routing metadata and event identities from authoritative columns, rejecting forged shortcut flags. Totals, inventory eligibility and PO reference eligibility all use the same completed-approval predicate.

## Expenses, costs, vendor onboarding and payments

- Expenses capture description, category (choose or type), payee, date, amount, remarks, optional related order and versioned custom fields. Presets include consultant charges, salaries, material purchase, admin costs, labour, rental, transport, utilities, site overheads and insurance. Drafts are editable only by their creator or Superuser; submitted records and approvals are retained. Both expense and payment approvals follow policy 2 above.
- Additional approved expenses increase project cost. Expenses linked to an approved work order or approved PO are marked already covered and excluded from additional cost. Payments settle invoices and do not increase project cost. Cost overview is restricted to a project Admin with cost-view permission or platform Superuser; it reports WO commitments, approved POs/rentals, additional approved expenses, category totals, net paid, TDS and outstanding invoices. Granting only the cost-view permission to Finance/Manager does not confer Admin access.
- Vendor onboarding allows suggested or free-text vendor types/trades. It validates contact information, phone, optional email, address, GSTIN/PAN format with at least one of PAN or GSTIN required and notes. Admin defaults allow prepare/edit own draft and submit; Superuser can activate a submitted profile, return it to draft or reject it with a reason. Draft/Submitted/Rejected vendors are unavailable for new work orders, POs or invoices. Existing directory records remain active. Tax IDs are format-checked, not verified against an authority.
- Payments has an immutable invoice register plus an approval-based settlement register. Select a project work order to populate its number/date and vendor. Capture invoice number/date, description, category and gross invoice value. Vendor invoice numbers are unique, case-insensitively, per vendor/project. Total invoice values for an order cannot exceed its effective value when an invoice is registered.
- Vendor payment rows include description, category, invoice Open/Closed status, work-order number/date, invoice number/date, net paid amount, TDS amount in rupees, paid date, balance and remarks. Amounts allow two decimals, cannot be negative, and paid date cannot precede invoice date. Net paid plus TDS is the settlement amount; no TDS rate is assumed. Partial payments are supported. Pending and rejected settlements do not affect balance. Final approval revalidates the outstanding balance under an invoice lock so concurrent pending settlements cannot overpay.
- A fully settled invoice automatically becomes Closed. Authorized payment-amend users can reopen it, or close it only at zero balance, with a required reason. Status events and their actors/timestamps remain visible in Amend status; direct rewrites are blocked. This records payments; it does not send funds.
- The Expense and Vendor Payment templates accept configurable additional text/number/date fields with immutable versions. The role editor includes these forms, onboarding fields and cost-view permission. Onboarding core fields are fixed; its additional template fields are not connected to intake entry. Cost overview is a fixed report.
- Migration 008 adds all tables, validation/approval triggers, tenant policies, new default permissions and vendor activation guards. Docker Compose runs it before the API starts, preserving earlier migrations and records. Go migration grants include the new tables.

Try Expenses as Rohith: consultant fees are approved, salaries await the mandatory Superuser after Finance, and admin costs are a draft. In Payments, submit and directly approve the partial-payment draft; balance reduces by net paid plus TDS. Record the remaining settlement to close the invoice. Open Cost overview to compare costs and settlements. In Vendor onboarding, open the submitted Aster consultant and activate it, or prepare a profile with a custom trade. To use onboarding and costs as another account, assign the separate Admin role in Users & roles. Hosted changes still reset on refresh.


## Vendors & Suppliers and CRM

The navigation groups Vendor Registry, Vendor Onboarding, Vendor Feedback, Work Orders, Vendor Quotations and Reporting. All entry forms support versioned text, number and date metadata. Superuser publishes fields in Form configuration, then grants hidden/view/edit access in Users & roles. Core reference, approval, amount and attachment rules remain mandatory. Existing records retain their template version; adding a required field affects new records. Read-only custom values survive editing other permitted fields. Dashboard and Reporting are configurable permission views; custom fields are entered on their underlying records.

- Collect vendor quotations against a work package with scope, contract type and an optional existing WO reference. Comparison shows price, delivery/execution days, credit days and terms without scores or ranking. The reviewer selects a quotation with a retained rationale. All submitted quotations display their work-order path and the selected quotation can raise a WO after approval. Selected quotations and their decision history cannot be overwritten; use a new revision/package when scope changes.
- Approve the quotation selection before raising a new WO. Its selected quotation ID is mandatory, must belong to the same project and vendor, and cannot be reused for another WO. The original WO value cannot exceed that quotation. Quotation selections and new WOs use the same Superuser/direct or regular two-person approval rules as other financial documents.
- Material construction WOs require an attached signed BOQ before saving. PDF, PNG or JPEG files up to 1 MiB are accepted; the creator attests that the attachment is signed. This does not verify a digital signature. Docker retains file bytes and SHA-256 in PostgreSQL; downloads require work-order, contract and financial-value visibility. Sample attachments last only until refresh.
- Labour sequence: approve quotation selection → save WO draft → raise linked PO → approve PO → approve WO. A material PO requires an approved material WO. Every new or edited PO needs its project WO, an overall purchase purpose, and a reason for each material/rental line. Reporting shows this purpose trail. Original legacy WOs remain usable without invented quotations or attachments; new records follow the new rules.
- Feedback records dated quality, timeliness, safety and communication assessments, optional vendor WO, comments and custom metadata. New assessments preserve earlier feedback. Vendor reporting includes quotation outcomes, approved WO and PO values, settlements, TDS, outstanding invoices, ratings and CSV export according to field permissions.
- CRM records include retained budget/target versions, dated work measurements, unit sales and confirmed cash collections. Progress is weighted by approved effective WO values; plots compare measurements with monthly plans. Sales show leads, reservations, bookings and cancellations. Collections link to booked sales and cannot exceed the sale value. A collected sale cannot be cancelled or reduced below receipts.
- The project budget is a separate target. Commitments equal approved effective WOs + approved material/rental POs + approved additional expenses. Linked expenses and payments do not add the same cost again. This does not reconcile duplicate scope between a WO and PO; enter WO rates for their separate contracted scope. Budget/cost insights require Admin or Superuser access plus configured view permissions. Progress, cash and sales insights have independent permissions.
- Hosted illustrative progress, sales and cash targets are manually editable sample data. The workbook was used as a specification, not imported as financial transactions. There is no bank, accounting or sales-system sync. Docker starts from the existing database seed; add project targets, sales and progress through these forms.
- Migration 009 adds project-isolated operational records, quotation-selection and WO approvals, signed BOQ retention, purpose references, receipt checks and metadata validation. Docker Compose applies it before API startup, without rewriting earlier migrations.

### Try the new sequence

1. Open CRM dashboard as Rohith to review progress, collections and commitments; use Budget & targets, Update progress, Add sale and Record collection to change its underlying records.
2. Open Vendor Quotations. The sample plastering package contains three bids and an approved selection; its selected quotation can raise a labour WO draft.
3. Open the draft WO and choose Raise linked purchase order. Fill the overall purpose and each line's purpose, submit and directly approve the PO as Rohith, then submit and approve the WO. The draft stays out of WO commitments until approval.
4. For material construction, create another work package, add bids, approve a selection, then attach its signed BOQ while raising the WO.
5. Add feedback and inspect Reporting → Purchase purpose trail. Publish a required metadata field, assign its role permissions, and confirm it appears on new forms.

Validation covers frontend domain rules, PostgreSQL migrations/guards, Go validators/redaction and rendered Vue/Vuetify screens. Docker Compose itself has not been executed in this workspace.


## Corporate accounts, booking profiles and payment schedules

- Corporate bank accounts are company-wide and Superuser-managed. Numbers are unique across corporate accounts and every project’s RERA accounts, including archived corporate accounts. The shared PostgreSQL reservation table enforces this across concurrent inserts. At least one active corporate account must remain after registration. Account numbers are retained; register a new account to change one.
- Salary/admin payments link an active corporate account to an approved project expense. Cash plus TDS cannot exceed that expense’s remaining balance; the balance is rechecked on submit and approval. Regular users need review and Superuser + Admin or Finance approval; Superuser-created drafts require explicit submit and Superuser approval. Payments settle expenses and never add project cost again. These entries record payments; the ERP does not initiate bank transfers.
- Work Order and Purchase Order templates now have `itemFields` alongside header `fields`. Superuser publishes optional or required text/number/date fields and grants `item_<key>` permissions on the corresponding role. Optional fields can be filled on just Item 1. Required fields apply to every item. New versions affect future documents; existing drafts retain their original template version. Item types, required values, permission checks and redaction apply in Vue, Go and PostgreSQL.
- CRM Bookings collect unit particulars, enquiry/lead IDs, applicant and co-applicant contact/identity/address details, optional passport photos, occupation/business, company/designation, channel-partner/referral source and sales/relationship-manager information. Photos must be PNG/JPEG up to 256 KiB each. Booking payment instrument details are informational; confirmed cleared money must be recorded in Cash collections. Hosted data uses illustrative applicants, never the identities from uploaded forms.
- Create a Booked unit in CRM, complete its booking profile, then generate a unit schedule. Booking pricing includes labelled Part A consideration, configurable GST/TDS basis points and individually taxed Part B possession charges. Saving a booking atomically updates its sale value to Part A + GST + Part B and updates the applicant name. Once scheduled, the sale unit/value/date/stage and booking pricing/wing/date are retained. Contact details remain editable by the creator or Superuser with the necessary permissions.
- The reference template has 18 Part A instalments totalling 100%: booking 5%, agreement 15%, excavation 5%, footing 6%, ground floor 6%, parking 5%, eight floor-slab stages at 5% each, superstructure 5%, flooring/painting 4%, electrical/plumbing 4% and registration 5%. Booking is due on its date; agreement after 5 days; construction milestones after 15 days; registration and Part B possession on their milestone dates. Superuser can publish new schedule versions. The sample’s GST/TDS inputs are editable values from the supplied reference, not a tax determination.
- Schedule amounts derive from the pre-GST Part A consideration, with GST/TDS separate and Part B due at possession. Cumulative integer-paise allocation reconciles every component to its original total without negative rounding residues. PostgreSQL independently verifies the schedule against the booking and template.
- Authorized users record milestone completion by project or wing/block. Latest matching-wing completion takes precedence over project-wide completion. Future completion dates are rejected. Completion corrections affect unraised instalments only. Managers can record milestones and view dates while financial values remain hidden by default.
- Raise a demand only when its due date is known, with a project RERA receiving account. Demand amounts, customer/unit, bank details and due dates are retained snapshots. Demands follow the same explicit financial approval rules. Approved demands can be viewed/printed to PDF; no email is sent automatically. The CRM shows issued and overdue demand balances.
- Record confirmed incoming net cash and optional TDS in CRM collections. Cash plots reflect net cash only. Receipt allocations associate cash/TDS with approved demands for the same unit, including receipts entered before the demand. Both receipt and demand caps are enforced; allocation never adds another cash inflow. Allocation and approval histories are retained.
- Migration 010 adds these tables, global account reservations, schedule and settlement validation, immutable event histories, tenant policies and configurable permissions. Docker Compose applies migrations automatically before the API starts. Existing migrations are unchanged. The hosted sample remains an in-memory demonstration that resets on refresh; durable operation uses the supplied Go/PostgreSQL Docker stack.
