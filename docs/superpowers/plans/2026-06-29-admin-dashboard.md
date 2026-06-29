# Admin Dashboard (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An admin-only section of the dashboard at `https://app.brezelscraper.com/admin` where admins manage promo codes (create / list / disable), built on a reusable admin shell + a trustworthy role signal.

**Architecture:** New backend `GET /api/v1/me` returns the caller's DB-authoritative `{id, role, tier}` from request context (no DB read). The frontend gains a `patch` method, a `useUserRole()` hook (SWR on `/me`), a role-gated "Admin" sidebar item, and an `/admin` route (rewritten to `/dashboard/admin`) whose client component gates on role and renders a `PromoCodeManager` using the **existing** admin promo endpoints. The backend `RequireRole(admin)` on `/api/v1/admin/*` remains the real security gate; frontend gating is UX only.

**Tech Stack:** Go (`gorilla/mux`, `database/sql`); Next.js App Router, TypeScript, SWR, Clerk, sonner, vitest.

**Spec:** `docs/superpowers/specs/2026-06-29-admin-dashboard-design.md` (read first).

**Branches:** backend `feat/admin-dashboard` (already created, off `develop`); create a matching `feat/admin-dashboard` in the frontend off its `develop`.

---

## Conventions (read once)
- **Backend** commands from `brezelscraper-backend/`. Build: `go build ./...`. Test a package: `go test ./web/handlers/ -run TestGetMe -v`.
- **Frontend** commands from `brezelscraper-frontend/`. `npm run typecheck` (tsc), `npx vitest run <file>`. NOTE: repo-wide `next lint` is broken (Next 16 / flat-config) — do NOT gate on it; keep code clean (no unused imports / `any`) and rely on typecheck + vitest.
- Apply **@golang-error-handling**, **@golang-testing**, **@golang-security** (backend) and follow existing frontend patterns.
- **Commit after each task.** Conventional Commits; end every message with:
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`
- **Security invariant (do not weaken):** `/api/v1/me` returns ONLY the caller's own identity (no `userId` param). The real admin gate is the backend `RequireRole(admin)` already on `/api/v1/admin/*`; the frontend role gate is cosmetic.

## File Structure
**Backend (`brezelscraper-backend/`)**
- Modify `models/api_models.go` — add `MeResponse` DTO.
- Create `web/handlers/me.go` — `APIHandlers.GetMe`.
- Create `web/handlers/me_test.go` — handler tests.
- Modify `web/web.go` — register `GET /api/v1/me`.

**Frontend (`brezelscraper-frontend/`)**
- Modify `src/lib/api/api-client.ts` — add `patch`.
- Modify `src/hooks/use-api.ts` — add `patch`.
- Modify `src/lib/swr.ts` — add `me` + `adminPromoCodes` keys.
- Create `src/hooks/useUserRole.ts` + `src/hooks/useUserRole.test.tsx`.
- Modify `next.config.js` — add `/admin` rewrite.
- Modify `src/lib/site.ts` — add `/admin` to `DASHBOARD_PATH_PREFIXES`.
- Create `src/app/dashboard/admin/page.tsx` + `AdminClient.tsx` + `AdminClient.test.tsx`.
- Modify `src/components/navigation/Sidebar.tsx` — conditional Admin item.
- Create `src/components/admin/PromoCodeManager.tsx` + `PromoCodeManager.test.tsx`.

---

## Chunk 1: Backend — `GET /api/v1/me`

### Task 1: `MeResponse` DTO + `GetMe` handler + route

**Files:**
- Modify: `models/api_models.go`
- Create: `web/handlers/me.go`
- Create: `web/handlers/me_test.go`
- Modify: `web/web.go`

> Design note: role + tier are written into the request context by the auth middleware (DB-authoritative) on every request, so `GetMe` reads them from context with **no DB query and no UserRepo mock**. Per the spec, we return `{id, role, tier}`; `email` is intentionally omitted (YAGNI — it's the only field needing a `GetByID`, and nothing in Phase 1 uses it).

- [ ] **Step 1: Add the DTO**

In `models/api_models.go`, near the other response structs, add:
```go
// MeResponse is the GET /api/v1/me payload: the caller's own identity.
// Sourced from the request context (DB-authoritative role/tier). A dedicated
// struct (not models.User) because User.Tier is json:"-" and User exposes
// fields we don't want to leak here.
type MeResponse struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Tier string `json:"tier"`
}
```

- [ ] **Step 2: Write the failing handler test**

`web/handlers/me_test.go`:
```go
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gosom/google-maps-scraper/models"
	"github.com/gosom/google-maps-scraper/web/auth"
)

func TestGetMe_ReturnsIdentityFromContext(t *testing.T) {
	h := &APIHandlers{Deps: Dependencies{Auth: &auth.AuthMiddleware{}}}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	ctx := context.WithValue(req.Context(), auth.UserIDKey, "user-abc")
	ctx = context.WithValue(ctx, auth.UserRoleKey, models.RoleAdmin)
	ctx = context.WithValue(ctx, auth.UserTierKey, models.UserTierPaid)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.GetMe(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got models.MeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "user-abc" || got.Role != models.RoleAdmin || got.Tier != models.UserTierPaid {
		t.Fatalf("unexpected body: %+v", got)
	}
}

func TestGetMe_Unauthenticated401(t *testing.T) {
	h := &APIHandlers{Deps: Dependencies{Auth: &auth.AuthMiddleware{}}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil) // no identity in ctx
	w := httptest.NewRecorder()

	h.GetMe(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestGetMe_DefaultsRoleToUser(t *testing.T) {
	h := &APIHandlers{Deps: Dependencies{Auth: &auth.AuthMiddleware{}}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	// Only a user id in context; no role/tier set.
	req = req.WithContext(context.WithValue(req.Context(), auth.UserIDKey, "u1"))
	w := httptest.NewRecorder()

	h.GetMe(w, req)

	var got models.MeResponse
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Role != models.RoleUser || got.Tier != models.UserTierFree {
		t.Fatalf("expected safe defaults user/free, got %+v", got)
	}
}
```

- [ ] **Step 3: Run it — verify it fails**

Run: `go test ./web/handlers/ -run TestGetMe -v`
Expected: FAIL/compile error (`h.GetMe` undefined).

- [ ] **Step 4: Implement the handler**

`web/handlers/me.go` (mirrors `APIHandlers.GetDashboard`'s auth guards):
```go
package handlers

import (
	"net/http"

	"github.com/gosom/google-maps-scraper/models"
	"github.com/gosom/google-maps-scraper/web/auth"
)

// GetMe returns the authenticated caller's own identity (id, DB-authoritative
// role, tier), read from the request context set by the auth middleware. No DB
// read; no userId param (returns only the caller — never another user).
func (h *APIHandlers) GetMe(w http.ResponseWriter, r *http.Request) {
	if h.Deps.Auth == nil {
		renderJSON(w, http.StatusUnauthorized, models.APIError{Code: http.StatusUnauthorized, Message: "Authentication not configured"})
		return
	}
	userID, err := auth.GetUserID(r.Context())
	if err != nil || userID == "" {
		renderJSON(w, http.StatusUnauthorized, models.APIError{Code: http.StatusUnauthorized, Message: "User not authenticated"})
		return
	}
	renderJSON(w, http.StatusOK, models.MeResponse{
		ID:   userID,
		Role: auth.GetUserRole(r.Context()),
		Tier: auth.GetUserTier(r.Context()),
	})
}
```

- [ ] **Step 5: Run tests — verify pass**

Run: `go test ./web/handlers/ -run TestGetMe -v` → PASS (all three).

- [ ] **Step 6: Register the route**

In `web/web.go`, immediately after the `/dashboard` registration (the line
`apiRouter.HandleFunc("/dashboard", hg.API.GetDashboard).Methods(http.MethodGet)`), add:
```go
	apiRouter.HandleFunc("/me", hg.API.GetMe).Methods(http.MethodGet)
```
(`/me` only needs auth — it goes on `apiRouter`, NOT `adminRouter`.)

- [ ] **Step 7: Build + commit**

Run: `go build ./...` (clean).
```bash
git add models/api_models.go web/handlers/me.go web/handlers/me_test.go web/web.go
git commit -m "feat(admin): GET /api/v1/me returns caller identity (id, role, tier)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Chunk 2: Frontend foundation — `patch` + `useUserRole`

> Switch to the frontend repo. Create the branch:
> `cd ../brezelscraper-frontend && git fetch origin develop && git checkout -b feat/admin-dashboard origin/develop`

### Task 2: Add `patch` to the API client + `useAPI`

**Files:**
- Modify: `src/lib/api/api-client.ts`
- Modify: `src/hooks/use-api.ts`

- [ ] **Step 1: Add `patch` to `APIClient`**

In `src/lib/api/api-client.ts`, right after the `put` method (mirrors it):
```ts
  async patch<T = unknown>(endpoint: string, data: unknown, getToken: GetTokenFn): Promise<T> {
    return this.request<T>(endpoint, { method: 'PATCH', body: JSON.stringify(data) }, getToken);
  }
```

- [ ] **Step 2: Add `patch` to the `useAPI` hook**

In `src/hooks/use-api.ts`, add to the returned memo object after `put` (mirrors it):
```ts
    patch: async <T = unknown>(endpoint: string, data: unknown): Promise<T> => {
      if (!isLoaded || !isSignedIn) {
        const token = await getToken();
        if (!token) throw new Error('Not authenticated');
      }
      return apiClient.patch<T>(endpoint, data, getToken);
    },
```

- [ ] **Step 3: Typecheck + commit**

Run: `npm run typecheck` (clean).
```bash
git add src/lib/api/api-client.ts src/hooks/use-api.ts
git commit -m "feat(api): add patch method to apiClient + useAPI

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

### Task 3: `SWR_KEYS` + `useUserRole()` hook

**Files:**
- Modify: `src/lib/swr.ts`
- Create: `src/hooks/useUserRole.ts`
- Create: `src/hooks/useUserRole.test.tsx`

- [ ] **Step 1: Add SWR keys**

In `src/lib/swr.ts`, inside the `SWR_KEYS` object (keep the `as const`), add:
```ts
  me: "/api/v1/me",
  adminPromoCodes: "/api/v1/admin/promo-codes",
```

- [ ] **Step 2: Write the failing hook test**

`src/hooks/useUserRole.test.tsx`:
```tsx
import { describe, it, expect, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { useUserRole } from "./useUserRole";

const { mockUseSWR, mockUseAuth } = vi.hoisted(() => ({
  mockUseSWR: vi.fn(),
  mockUseAuth: vi.fn(),
}));
vi.mock("swr", () => ({ default: (...args: unknown[]) => mockUseSWR(...args) }));
vi.mock("@clerk/nextjs", () => ({ useAuth: () => mockUseAuth() }));
vi.mock("~/lib/swr", () => ({ createFetcher: () => vi.fn(), SWR_KEYS: { me: "/api/v1/me" } }));

describe("useUserRole", () => {
  it("isAdmin true when /me returns role admin", () => {
    mockUseAuth.mockReturnValue({ getToken: vi.fn(), isLoaded: true, isSignedIn: true });
    mockUseSWR.mockReturnValue({ data: { id: "u", role: "admin", tier: "paid" }, isLoading: false });
    const { result } = renderHook(() => useUserRole());
    expect(result.current.isAdmin).toBe(true);
    expect(result.current.role).toBe("admin");
  });

  it("fails closed: isAdmin false while loading / for user role / on no data", () => {
    mockUseAuth.mockReturnValue({ getToken: vi.fn(), isLoaded: true, isSignedIn: true });
    mockUseSWR.mockReturnValue({ data: undefined, isLoading: true });
    const { result } = renderHook(() => useUserRole());
    expect(result.current.isAdmin).toBe(false);
    expect(result.current.isLoading).toBe(true);
  });
});
```

- [ ] **Step 3: Run it — verify it fails**

Run: `npx vitest run src/hooks/useUserRole.test.tsx`
Expected: FAIL (module not found).

- [ ] **Step 4: Implement the hook** (mirrors `useCredits` SWR setup)

`src/hooks/useUserRole.ts`:
```ts
"use client";

import { useMemo } from "react";
import useSWR from "swr";
import { useAuth } from "@clerk/nextjs";
import { createFetcher, SWR_KEYS } from "~/lib/swr";

interface MeResponse {
  id: string;
  role: string;
  tier: string;
}

/**
 * Reads the caller's DB-authoritative role from GET /api/v1/me.
 * Fails closed: isAdmin is false until the role is confirmed "admin".
 * NOTE: this only controls UX (nav + page gating); the backend
 * RequireRole(admin) on /api/v1/admin/* is the real access gate.
 */
export function useUserRole() {
  const { getToken, isLoaded, isSignedIn } = useAuth();
  const fetcher = useMemo(() => createFetcher(getToken), [getToken]);

  const { data, isLoading } = useSWR<MeResponse>(
    isLoaded && isSignedIn ? SWR_KEYS.me : null,
    fetcher,
    { revalidateOnFocus: false, dedupingInterval: 60_000 },
  );

  const role = data?.role ?? "user";
  return {
    role,
    isAdmin: role === "admin",
    isLoading: !isLoaded || !isSignedIn || isLoading,
  };
}
```

- [ ] **Step 5: Run + typecheck + commit**

Run: `npx vitest run src/hooks/useUserRole.test.tsx` → PASS; `npm run typecheck` → clean.
```bash
git add src/lib/swr.ts src/hooks/useUserRole.ts src/hooks/useUserRole.test.tsx
git commit -m "feat(admin): useUserRole hook + /me, adminPromoCodes SWR keys

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Chunk 3: Admin route + role-gated nav

### Task 4: `/admin` route + rewrite + access gate

**Files:**
- Modify: `next.config.js`
- Modify: `src/lib/site.ts`
- Create: `src/app/dashboard/admin/page.tsx`
- Create: `src/app/dashboard/admin/AdminClient.tsx`
- Create: `src/app/dashboard/admin/AdminClient.test.tsx`

- [ ] **Step 1: Add the rewrite**

In `next.config.js` `rewrites()`, after the `/integrations` line, add:
```js
      { source: '/admin/:path*', destination: '/dashboard/admin/:path*' },
```

- [ ] **Step 2: Treat `/admin` as dashboard chrome**

In `src/lib/site.ts`, add `"/admin"` to the `DASHBOARD_PATH_PREFIXES` array (after `"/integrations"`).

- [ ] **Step 3: Server page (mirrors `credits/page.tsx`)**

`src/app/dashboard/admin/page.tsx`:
```tsx
import type { Metadata } from "next";
import { AdminClient } from "./AdminClient";

export const metadata: Metadata = {
  title: "Admin – BrezelScraper",
  robots: { index: false, follow: false },
};

export default function AdminPage() {
  return <AdminClient />;
}
```

- [ ] **Step 4: Write the failing gate test**

`src/app/dashboard/admin/AdminClient.test.tsx`:
```tsx
import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { AdminClient } from "./AdminClient";

const { mockUseUserRole } = vi.hoisted(() => ({ mockUseUserRole: vi.fn() }));
vi.mock("~/hooks/useUserRole", () => ({ useUserRole: () => mockUseUserRole() }));
// PromoCodeManager makes API calls; stub it so the gate test stays isolated.
vi.mock("~/components/admin/PromoCodeManager", () => ({
  PromoCodeManager: () => <div data-testid="promo-manager" />,
}));

describe("AdminClient gate", () => {
  it("shows Not authorized for non-admins and does NOT render the manager", () => {
    mockUseUserRole.mockReturnValue({ isAdmin: false, isLoading: false, role: "user" });
    render(<AdminClient />);
    expect(screen.getByRole("alert")).toHaveTextContent(/not authorized/i);
    expect(screen.queryByTestId("promo-manager")).toBeNull();
  });

  it("renders the manager for admins", () => {
    mockUseUserRole.mockReturnValue({ isAdmin: true, isLoading: false, role: "admin" });
    render(<AdminClient />);
    expect(screen.getByTestId("promo-manager")).toBeInTheDocument();
  });

  it("shows a loading state while role is resolving (no manager yet)", () => {
    mockUseUserRole.mockReturnValue({ isAdmin: false, isLoading: true, role: "user" });
    render(<AdminClient />);
    expect(screen.queryByTestId("promo-manager")).toBeNull();
  });
});
```

- [ ] **Step 5: Run it — verify it fails**

Run: `npx vitest run src/app/dashboard/admin/AdminClient.test.tsx` → FAIL (module not found).

- [ ] **Step 6: Implement `AdminClient`** (mirrors `CreditsClient` shell)

`src/app/dashboard/admin/AdminClient.tsx`:
```tsx
"use client";

import { Page } from "~/components/layout";
import { PageHeader } from "~/components/ui/PageHeader";
import { useUserRole } from "~/hooks/useUserRole";
import { PromoCodeManager } from "~/components/admin/PromoCodeManager";

export function AdminClient() {
  const { isAdmin, isLoading } = useUserRole();

  if (isLoading) {
    return (
      <Page width="6xl">
        <div className="p-8 text-sm text-t-label">Loading…</div>
      </Page>
    );
  }

  if (!isAdmin) {
    return (
      <Page width="6xl">
        <div
          role="alert"
          className="mt-6 rounded-xl border border-red-200 bg-red-50 p-5 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
        >
          Not authorized. This area is for administrators only.
        </div>
      </Page>
    );
  }

  return (
    <Page width="6xl">
      <PageHeader>
        <PageHeader.Text>
          <PageHeader.Title>Admin</PageHeader.Title>
          <PageHeader.Subtitle>Manage promo codes.</PageHeader.Subtitle>
        </PageHeader.Text>
      </PageHeader>
      <PromoCodeManager />
    </Page>
  );
}
```
> NOTE: `PromoCodeManager` is built in Task 6. To keep this task's tests green now, create a temporary stub `src/components/admin/PromoCodeManager.tsx` exporting `export function PromoCodeManager() { return null; }`, then flesh it out in Task 6. (The test mocks it regardless.)

- [ ] **Step 7: Run + typecheck + commit**

Run: `npx vitest run src/app/dashboard/admin/AdminClient.test.tsx` → PASS; `npm run typecheck` → clean.
```bash
git add next.config.js src/lib/site.ts src/app/dashboard/admin/ src/components/admin/PromoCodeManager.tsx
git commit -m "feat(admin): /admin route + rewrite + role-gated AdminClient shell

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

### Task 5: Role-gated "Admin" sidebar item

**Files:**
- Modify: `src/components/navigation/Sidebar.tsx`
- Create: `src/components/navigation/Sidebar.test.tsx` (if none exists)

- [ ] **Step 1: Add the Admin item, conditionally**

In `src/components/navigation/Sidebar.tsx`:
1. Add `Shield` to the `lucide-react` import.
2. Add `import { useUserRole } from "~/hooks/useUserRole";`.
3. Define an admin item near `navigationItems`:
```tsx
const adminItem = {
  name: "Admin",
  canonical: "/admin",
  icon: Shield,
  iconMotion: "group-hover:scale-110",
  description: "Admin tools",
} as const;
```
4. Inside the component, before the `return`:
```tsx
  const { isAdmin } = useUserRole();
  const items = isAdmin ? [...navigationItems, adminItem] : navigationItems;
```
5. Change the nav map from `navigationItems.map(...)` to `items.map(...)`.

- [ ] **Step 2: Test (mock useUserRole)**

`src/components/navigation/Sidebar.test.tsx` — render with `useUserRole` mocked; assert the "Admin" link is present when `isAdmin` true and absent when false. Mock `next/navigation` `usePathname` (return `"/"`), and `~/lib/site` if needed. Follow the `vi.hoisted` + `vi.mock` style from `PromoCodeRedemption.test.tsx`. (If mocking the rich Sidebar deps is heavy, at minimum assert presence/absence of text "Admin".)

- [ ] **Step 3: Run + typecheck + commit**

Run: `npx vitest run src/components/navigation/Sidebar.test.tsx` → PASS; `npm run typecheck` → clean.
```bash
git add src/components/navigation/Sidebar.tsx src/components/navigation/Sidebar.test.tsx
git commit -m "feat(admin): show Admin sidebar item only to admins

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Chunk 4: Promo-code management UI

### Task 6: `PromoCodeManager` — list / create / disable

**Files:**
- Modify (replace stub): `src/components/admin/PromoCodeManager.tsx`
- Create: `src/components/admin/PromoCodeManager.test.tsx`

Uses the **existing** admin endpoints: `GET`/`POST /api/v1/admin/promo-codes`, `PATCH /api/v1/admin/promo-codes/{id}` `{status:"disabled"}` (returns `204` — no body). Uses the `Table` family (`~/components/ui/table`), `Button`, `Input`, `toast`, `useAPI`, and SWR (`SWR_KEYS.adminPromoCodes`).

- [ ] **Step 1: Write the failing tests**

`src/components/admin/PromoCodeManager.test.tsx` — mirror `PromoCodeRedemption.test.tsx` mocking style (`vi.hoisted` mocks for `useAPI`, `sonner`, and `swr`). Cover:
- **List renders rows:** mock SWR to return two codes → assert both `code` strings appear and `current_redemptions`/`max_redemptions` render.
- **Create posts payload + revalidates + toasts:** fill the form (code, amount), submit → `post` called with `/api/v1/admin/promo-codes` and the parsed payload (amount as number); `mutate` called; success toast.
- **Duplicate (409) shows inline error:** `post` rejects with `new ApiError(409, "a promo code with that code already exists", {code:409, message:...})` → inline `role="alert"` shows the message; no success toast.
- **Disable PATCHes + revalidates:** click "Disable" on an active row → `patch` called with `/api/v1/admin/promo-codes/{id}` and `{status:"disabled"}`; `mutate` called; success toast. (Mock `patch` on the `useAPI` mock; assert it does not try to parse a body.)

- [ ] **Step 2: Run — verify fail**

Run: `npx vitest run src/components/admin/PromoCodeManager.test.tsx` → FAIL.

- [ ] **Step 3: Implement `PromoCodeManager`**

Replace the stub with the component. Requirements (complete code — match house style: token-class divs, `Table` family, `Button`, `Input`, sonner, `ApiError` message extraction mirroring `PromoCodeRedemption`):
- Types:
  ```ts
  interface PromoCode {
    id: string; code: string; amount: number; description?: string | null;
    status: "active" | "disabled"; max_redemptions?: number | null;
    current_redemptions: number; new_accounts_only: boolean;
    valid_from: string; valid_to?: string | null; created_at: string;
  }
  ```
- **List:** `const { data, mutate } = useSWR<PromoCode[]>(SWR_KEYS.adminPromoCodes, createFetcher(getToken))` (build fetcher via `useAuth().getToken` like `useCredits`), render a `<Table>` with columns: Code, Amount, Status, Redemptions (`current_redemptions`/`max_redemptions ?? "∞"`), Valid To, Action. Active rows show a "Disable" `Button` (`variant="danger" size="sm"`); disabled rows show a muted "Disabled" label.
- **Create form:** a `<form onSubmit>` with `Input`s for code (uppercased on change), amount (number), description (optional), max_redemptions (optional number), a `new_accounts_only` checkbox, valid_to (optional `datetime-local`). On submit → build payload (`amount: Number(...)`, omit empty optionals; `valid_to` → ISO string if set), `await api.post("/api/v1/admin/promo-codes", payload)`, then `await mutate()`, success toast, reset form. On error use `extractErrorMessage(err)` (copy the helper from `PromoCodeRedemption.tsx`) → inline `role="alert"` + `toast.error`.
- **Disable handler:** `await api.patch("/api/v1/admin/promo-codes/" + id, { status: "disabled" })` (do NOT read a response body — it's `204`), then `await mutate()` + toast. Wrap in try/catch with the same error extraction.
- Loading/empty states for the list; disable the create button while submitting and when code/amount empty.

- [ ] **Step 4: Run — verify pass**

Run: `npx vitest run src/components/admin/PromoCodeManager.test.tsx` → PASS.

- [ ] **Step 5: Typecheck + commit**

Run: `npm run typecheck` → clean.
```bash
git add src/components/admin/PromoCodeManager.tsx src/components/admin/PromoCodeManager.test.tsx
git commit -m "feat(admin): promo-code manager (list / create / disable)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

### Task 7: End-to-end verification

- [ ] **Step 1: Full frontend gates**

Run: `npm run typecheck` (clean) and `npx vitest run` (full suite green).

- [ ] **Step 2: Manual smoke (document in the PR)**

With the backend running (admin DB role via `promote_admin.sh`) and the frontend dev server:
- As a **non-admin**: no "Admin" sidebar item; visiting `/admin` shows "Not authorized"; (and any admin API call would 403).
- As an **admin**: "Admin" item appears; `/admin` shows the promo manager; create a code → appears in the list; disable it → status flips; create a duplicate → inline 409 error.
- Confirm `GET /api/v1/me` returns your `role` (curl with a Clerk JWT, or devtools network tab).

- [ ] **Step 3: Open PRs** (base `develop`) for both repos using @superpowers:finishing-a-development-branch; cross-link them. Note in each: backend `/me` is the only backend change; frontend is the admin UI.

---

## Out of scope (per spec §10) — do NOT build
System-health metrics / `/api/v1/admin/stats` (Phase 2) · admin user management (promote/demote) · promo redemptions detail view · hard-delete of codes · audit-log viewer · server-side (Clerk-metadata/edge) role gating.
