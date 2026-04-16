import { beforeEach, describe, expect, it, vi } from "vitest";

const {
  mockClearLoggedInCookie,
  mockHasLoggedInCookie,
  mockSetLoggedInCookie,
  mockApiLogout,
  mockSetToken,
  mockSetWorkspaceId,
} = vi.hoisted(() => ({
  mockClearLoggedInCookie: vi.fn(),
  mockHasLoggedInCookie: vi.fn(),
  mockSetLoggedInCookie: vi.fn(),
  mockApiLogout: vi.fn(),
  mockSetToken: vi.fn(),
  mockSetWorkspaceId: vi.fn(),
}));

vi.mock("@/shared/api", () => ({
  api: {
    logout: mockApiLogout,
    setToken: mockSetToken,
    setWorkspaceId: mockSetWorkspaceId,
  },
}));

vi.mock("./auth-cookie", () => ({
  clearLoggedInCookie: mockClearLoggedInCookie,
  hasLoggedInCookie: mockHasLoggedInCookie,
  setLoggedInCookie: mockSetLoggedInCookie,
}));

import { useAuthStore } from "./store";

describe("auth store logout", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    useAuthStore.setState({ user: null, isLoading: false });
  });

  it("calls server logout before clearing local auth state", async () => {
    mockApiLogout.mockResolvedValueOnce(undefined);
    localStorage.setItem("multica_token", "stale-token");

    await useAuthStore.getState().logout();

    expect(mockApiLogout).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem("multica_token")).toBeNull();
    expect(mockSetToken).toHaveBeenCalledWith(null);
    expect(mockSetWorkspaceId).toHaveBeenCalledWith(null);
    expect(mockClearLoggedInCookie).toHaveBeenCalledTimes(1);
    expect(useAuthStore.getState().user).toBeNull();
  });
});
