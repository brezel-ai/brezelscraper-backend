# Admin Dashboard — Design Spec (Phase 1)

- **Date:** 2026-06-29
- **Status:** Draft (pending review)
- **Author:** Engineering
- **Scope:** `brezelscraper-frontend` (admin UI) + `brezelscraper-backend` (one new endpoint)

## 1. Summary

A secure, admin-only section of the existing dashboard app, reachable at
`https://app.brezelscraper.com/admin`, where admins manage **promo codes** (create / list /
disable). It is the foundation that future admin features (system-health metrics, etc.) plug
into. **Phase 1 = the admin shell + access control + promo-code management.** System-health
metrics (user counts, running tasks, queue depth, health) are an explicit **Phase 2** fast-follow
(see §11).

## 2. Goals / Non-goals

**Goals**
- An `/admin` route inside the existing dashboard, visible only to admins, that inherits the
  current layout/auth/sidebar.
- A trustworthy way for the frontend to know the caller's role (to show the nav + gate the page).
- Promo-code management UI: create a code, list codes with redemption counts, disable a code.
- Hold the line on security: the backend remains the only real access gate.

**Non-goals (Phase 1)** — see §11.
- System-health/metrics dashboard (Phase 2).
- Admin **user** management (promote/demote) — stays script-only (`promote_admin.sh`).
- Promo redemptions detail view, hard-delete of codes, admin audit-log viewer.

## 3. Context (audit)

- **Role is DB-authoritative.** `users.role IN ('user','admin')` (migration `000028`). The auth
  middleware (`web/auth/auth.go`) reads `dbUser.Role` from Postgres on **every** request (Clerk
  JWT and API-key paths). It never auto-grants admin: a transient DB error on the JWT path
  rejects the request (HTTP 500), and the `GetUserRole` accessor defaults to `'user'` when no role
  is in context (least privilege).
  Clerk is **not** a role source. `auth.IsAdmin(ctx)` / `auth.GetUserRole(ctx)` expose it.
- **Admin role is granted only by `scripts/promote_admin.sh`** (direct DB, manual confirmation,
  sets `role='admin'` + `max_concurrent_jobs=50`). There is deliberately **no API for
  self-promotion**.
- **Admin API surface already exists and is gated.** `web/web.go` mounts an `adminRouter` with
  `RequireRole(models.RoleAdmin)`, and every handler additionally calls `requireAdminSession`
  (`web/handlers/admin.go`) which rejects non-admins **and API-key auth** (session-only).
  Existing routes: `/api/v1/admin/jobs` (×3) and **`/api/v1/admin/promo-codes`** —
  `POST` (create), `GET` (list, returns `current_redemptions`), `PATCH /{id}` (set status
  `active|disabled`). **The promo CRUD this feature needs already exists** (disable = `PATCH
  {status:"disabled"}`).
- **The gap:** the frontend has **no role signal**. `currentUser()` (Clerk server) and
  `useAuth()` (Clerk client) expose identity but not our DB role; there is no `/api/v1/me`.
- **Routing:** dashboard pages live under `src/app/dashboard/*`; `next.config.js` `rewrites()`
  map clean URLs onto them (`/jobs/:path*` → `/dashboard/jobs/:path*`, etc.). Auth gating is in
  `src/app/dashboard/layout.tsx` (server component; `currentUser()` → redirect to `/sign-in`).
  Nav is `src/components/navigation/Sidebar.tsx`. Data fetching is client-side SWR (`useAPI`,
  `createFetcher`, `SWR_KEYS` in `src/lib/swr.ts`); the Clerk JWT is attached by `apiClient`.
  `dashboardHref()` / `canonicalDashboardPath()` (`src/lib/site.ts`) build URLs + active state.
- **No `src/middleware.ts`** exists; route protection is layout-level.

## 4. The core security principle

**The backend is the security boundary; the frontend is UX.** `RequireRole(admin)` re-checks the
DB role on every admin API call, so the React layer (hiding the nav, gating the page) is
**cosmetic** — it cannot create a privilege-escalation hole because it is not the gate. A
non-admin who forces the `/admin` URL sees an empty shell whose every data call returns `403`.
This principle governs the whole design and is the reason client-side gating is acceptable.

## 5. Decisions (approved)

| # | Decision | Choice |
|---|---|---|
| D1 | Phase-1 scope | Admin shell + access control + promo management |
| D2 | URL | `/admin` on `app.brezelscraper.com` (rewrite to `/dashboard/admin`) |
| D3 | Role signal | New `GET /api/v1/me` (DB-backed) + `useUserRole()` hook; backend enforces |
| D4 | Admin-granting | Stays **script-only** (`promote_admin.sh`); not in the UI |
| D5 | Promo "remove" | **Disable** (`PATCH status=disabled`) — soft, reversible, preserves history |

## 6. Architecture

```
Sidebar (role-gated "Admin" link) ─┐
                                   ▼
 /admin  ──rewrite──▶ /dashboard/admin/page.tsx (server) ─▶ <AdminClient/> (client)
   │                                                          │
   │  useUserRole()  ── SWR ──▶ GET /api/v1/me ──▶ {id,email,role,tier}  (DB-backed)
   │      └─ role!=="admin" → render "Not authorized" (no admin API calls)
   │      └─ role==="admin" → render promo management
   │                                                          │
   └─ promo UI ── useAPI ──▶ /api/v1/admin/promo-codes  (GET list / POST create / PATCH disable)
                                   ▲ backend: RequireRole(admin) + requireAdminSession (REAL gate)
```

### 6.1 Backend — one new endpoint: `GET /api/v1/me`
- **Auth:** any authenticated user (mounted on `apiRouter`, not `adminRouter`). Returns the
  **caller's own** identity only — **no `userId` parameter** (avoids IDOR).
- **Response:** a **dedicated DTO** — do NOT serialize `models.User` directly (its `Tier` and
  `StripeCustomerID` are tagged `json:"-"`, so `tier` would be dropped, and fields like
  `RefundDeficitCredits` would leak). New struct, e.g. `MeResponse{ ID, Email, Role, Tier string }`
  → `{ "id": "...", "email": "...", "role": "user|admin", "tier": "free|paid" }`.
- **Handler:** a new method on an existing handler group (e.g. `APIHandlers.GetMe`):
  `auth.GetUserID(ctx)` + `UserRepo.GetByID` (the DB read keeps role/tier authoritative and
  yields email; role/tier are also in context via `auth.GetUserRole`/`GetUserTier`), then
  `renderJSON(w, 200, MeResponse{...})`. Registered:
  `apiRouter.HandleFunc("/me", hg.API.GetMe).Methods(http.MethodGet)`.
- No new admin endpoints — promo CRUD already exists.

### 6.2 Frontend
- **`src/lib/swr.ts`:** add `SWR_KEYS.me = "/api/v1/me"` and `SWR_KEYS.adminPromoCodes =
  "/api/v1/admin/promo-codes"`.
- **`src/hooks/useUserRole.ts` (new):** SWR on `SWR_KEYS.me`; returns
  `{ role, isAdmin, isLoading }`. Defaults to non-admin until loaded.
- **`src/components/navigation/Sidebar.tsx`:** conditionally append an "Admin" item
  (`dashboardHref("/admin")`, a shield icon) only when `isAdmin`.
- **`src/app/dashboard/admin/page.tsx` (new, server component):** mirrors other dashboard pages;
  renders `<AdminClient/>`.
- **`src/app/dashboard/admin/AdminClient.tsx` (new, client):** the access gate —
  `useUserRole()`: while loading → skeleton; if not admin → a "Not authorized" panel (and a
  redirect to `/` after a beat); if admin → the promo-management UI. No admin API call is made
  unless `isAdmin`.
- **`src/components/admin/PromoCodeManager.tsx` (new, client):**
  - **List:** SWR on `SWR_KEYS.adminPromoCodes` → table (code, amount, status,
    `current_redemptions`/`max_redemptions`, `valid_to`), newest first.
  - **Create:** a form (code, amount, description, max_redemptions, new_accounts_only, valid_to)
    → `useAPI().post("/api/v1/admin/promo-codes", …)` → `mutate(SWR_KEYS.adminPromoCodes)` +
    sonner toast. Maps backend errors (e.g. 409 `ErrPromoCodeExists`) to inline messages.
  - **Disable:** a "Disable" action per active row → `useAPI().patch("/api/v1/admin/promo-codes/{id}",
    {status:"disabled"})` → revalidate + toast. The endpoint returns **`204 No Content`** (no JSON
    body — the handler must not parse one). **`patch` does not exist yet**: add it in BOTH
    `src/lib/api/api-client.ts` (mirror the existing `put`) AND the `useAPI()` wrapper
    (`src/hooks/use-api.ts`). Small + mechanical; required because a POST would `405` on the
    PATCH-only route.
  - Reuses existing UI primitives (Input, Button, Card, table styling) + `t-*` design tokens.
- **`next.config.js`:** add `{ source: '/admin/:path*', destination: '/dashboard/admin/:path*' }`
  to `rewrites()`.

## 7. Security model / threats addressed

| Threat | Mitigation |
|---|---|
| Non-admin reaches `/admin` or calls admin APIs | Backend `RequireRole(admin)` + `requireAdminSession` (API keys rejected) → `403`; client gate is cosmetic |
| Role spoofing in the browser | Backend ignores client claims and reads DB role per request; faking the UI grants nothing |
| Privilege escalation via "add admin" | No such feature — admin-granting stays script-only (D4) |
| IDOR on `/api/v1/me` | Returns only the caller's own record; no `userId` param |
| Flash-of-admin-shell | Loading gate in `AdminClient`; no data leaks regardless (APIs 403) |
| Accountability | Admin **job** mutations are logged (Warn); promo create/disable logging is **added by the plan** (Task 1b) — the existing promo handlers logged only error paths. An audit-log **viewer** is Phase 2+ |
| CSRF | API uses bearer-JWT (not ambient cookies) → no classic CSRF vector |

## 8. Data flow & error handling
- Role: `AdminClient`/`Sidebar` → `useUserRole()` → SWR `/api/v1/me`. On error or while loading,
  treat as non-admin (fail closed).
- Promo list/create/disable: `useAPI` → admin endpoints; on success `mutate` the list key; on
  error show the server `message` inline + sonner toast (mirrors `PromoCodeRedemption`).
- A `403` from any admin call (e.g. role revoked mid-session) → show the "Not authorized" state.

## 9. Testing
**Backend**
- `GetMe` handler: returns the caller's own `{role,tier}`; unauthenticated → `401`; never reads
  a `userId` param. Table-driven, mirroring existing handler tests.

**Frontend (vitest + testing-library)**
- `useUserRole`: admin vs user vs loading vs error (fail-closed) — mock SWR.
- `AdminClient` gate: not-admin renders "Not authorized" and makes **no** promo API call;
  admin renders the manager.
- `PromoCodeManager`: list renders rows; create posts the right payload + revalidates + toasts;
  duplicate-code (409) shows inline error; disable PATCHes and revalidates.
- `Sidebar`: Admin link present iff `isAdmin`.

## 10. Out of scope (Phase 1)
Admin user management (promote/demote) · system-health metrics & `/api/v1/admin/stats` ·
promo redemptions detail view · hard-delete of codes · admin audit-log viewer · any change to how
admins are granted.

## 11. Phasing / future
- **Phase 2 — System health:** admin-gated `GET /api/v1/admin/stats` (cheap aggregates: users
  total/active/paid; jobs by status; running tasks; queue depth = pending; proxy-pool health;
  DB/version) + metric cards on `/admin`. Slots into the same shell with no rework.
- **Later (each its own spec):** guarded admin user-management (audit-logged, can't demote last
  admin, step-up auth) · admin audit-log viewer · deeper observability.

## 12. Resolved notes
- `patch` does **not** exist in `api-client.ts` or `use-api.ts` — add it to **both** (mirror
  `put`). Required for the disable action (a POST would `405` on the PATCH-only route).
- The disable PATCH returns `204 No Content` — no response body to parse.
- `/api/v1/me` uses a dedicated `MeResponse` DTO (never serialize `models.User`).
- Admin nav icon: Shield (Lucide, already in the icon set).
