import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, waitFor } from "@testing-library/react";

const {
  getMe,
  listWorkspaces,
  setToken,
  setWorkspaceId,
  hydrateWorkspace,
  authSetState,
  clearLoggedInCookie,
  setLoggedInCookie,
  hasLoggedInCookie,
} = vi.hoisted(() => ({
  getMe: vi.fn(),
  listWorkspaces: vi.fn(),
  setToken: vi.fn(),
  setWorkspaceId: vi.fn(),
  hydrateWorkspace: vi.fn(),
  authSetState: vi.fn(),
  clearLoggedInCookie: vi.fn(),
  setLoggedInCookie: vi.fn(),
  hasLoggedInCookie: vi.fn(),
}));

vi.mock("@/shared/api", () => ({
  api: {
    getMe,
    listWorkspaces,
    setToken,
    setWorkspaceId,
  },
}));

vi.mock("@/features/workspace", () => ({
  useWorkspaceStore: {
    getState: () => ({ hydrateWorkspace }),
  },
}));

vi.mock("./store", () => ({
  useAuthStore: {
    setState: authSetState,
  },
}));

vi.mock("./auth-cookie", () => ({
  clearLoggedInCookie,
  setLoggedInCookie,
  hasLoggedInCookie,
}));

import { AuthInitializer } from "./initializer";

describe("AuthInitializer", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    document.cookie = "multica_logged_in=1; path=/";
    hasLoggedInCookie.mockReturnValue(true);
  });

  it("bootstraps browser auth from the login cookie without a stored bearer token", async () => {
    listWorkspaces.mockResolvedValueOnce([{ id: "ws-1" }]);
    getMe.mockResolvedValueOnce({ id: "user-1", email: "user@example.com" });
    localStorage.setItem("multica_workspace_id", "ws-1");

    render(
      <AuthInitializer>
        <div>child</div>
      </AuthInitializer>,
    );

    await waitFor(() => {
      expect(getMe).toHaveBeenCalledTimes(1);
    });

    expect(setToken).not.toHaveBeenCalled();
    expect(listWorkspaces).toHaveBeenCalledTimes(1);
    expect(setLoggedInCookie).toHaveBeenCalledTimes(1);
    expect(authSetState).toHaveBeenCalledWith({
      user: { id: "user-1", email: "user@example.com" },
      isLoading: false,
    });
    expect(hydrateWorkspace).toHaveBeenCalledWith([{ id: "ws-1" }], "ws-1");
    expect(clearLoggedInCookie).not.toHaveBeenCalled();
  });
});
