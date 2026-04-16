import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const {
  mockPush,
  mockReplace,
  mockSearchParams,
  mockSendCode,
  mockVerifyCode,
  mockSetLoggedInCookie,
  mockHydrateWorkspace,
  mockListWorkspaces,
  mockApiVerifyCode,
  mockSetToken,
  mockGetMe,
  mockGetSessionToken,
} = vi.hoisted(() => ({
  mockPush: vi.fn(),
  mockReplace: vi.fn(),
  mockSearchParams: new URLSearchParams(),
  mockSendCode: vi.fn(),
  mockVerifyCode: vi.fn(),
  mockSetLoggedInCookie: vi.fn(),
  mockHydrateWorkspace: vi.fn(),
  mockListWorkspaces: vi.fn().mockResolvedValue([]),
  mockApiVerifyCode: vi.fn(),
  mockSetToken: vi.fn(),
  mockGetMe: vi.fn(),
  mockGetSessionToken: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: mockPush, replace: mockReplace }),
  usePathname: () => "/login",
  useSearchParams: () => mockSearchParams,
}));

vi.mock("@/features/auth", () => ({
  useAuthStore: (selector: (s: any) => any) =>
    selector({
      user: null,
      isLoading: false,
      sendCode: mockSendCode,
      verifyCode: mockVerifyCode,
    }),
  setLoggedInCookie: mockSetLoggedInCookie,
}));

vi.mock("@/features/workspace", () => ({
  useWorkspaceStore: (selector: (s: any) => any) =>
    selector({
      hydrateWorkspace: mockHydrateWorkspace,
    }),
}));

vi.mock("@/shared/api", () => ({
  api: {
    listWorkspaces: mockListWorkspaces,
    verifyCode: mockApiVerifyCode,
    setToken: mockSetToken,
    getMe: mockGetMe,
    getSessionToken: mockGetSessionToken,
  },
}));

import LoginPage from "./page";

describe("LoginPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockSearchParams.forEach((_, key) => {
      mockSearchParams.delete(key);
    });
    Object.defineProperty(window, "location", {
      configurable: true,
      value: {
        href: "http://localhost/login",
        assign: vi.fn((href: string) => {
          window.location.href = href;
        }),
      },
    });
  });

  it("renders login form with email input and continue button", () => {
    render(<LoginPage />);

    expect(screen.getByText("Multica")).toBeInTheDocument();
    expect(screen.getByText("Turn coding agents into real teammates")).toBeInTheDocument();
    expect(screen.getByLabelText("Email")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue" })).toBeInTheDocument();
  });

  it("does not call sendCode when email is empty", async () => {
    const user = userEvent.setup();
    render(<LoginPage />);

    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(mockSendCode).not.toHaveBeenCalled();
  });

  it("calls sendCode with email on submit", async () => {
    mockSendCode.mockResolvedValueOnce(undefined);
    const user = userEvent.setup();
    render(<LoginPage />);

    await user.type(screen.getByLabelText("Email"), "test@multica.ai");
    await user.click(screen.getByRole("button", { name: "Continue" }));

    await waitFor(() => {
      expect(mockSendCode).toHaveBeenCalledWith("test@multica.ai");
    });
  });

  it("shows 'Sending code...' while submitting", async () => {
    mockSendCode.mockReturnValueOnce(new Promise(() => {}));
    const user = userEvent.setup();
    render(<LoginPage />);

    await user.type(screen.getByLabelText("Email"), "test@multica.ai");
    await user.click(screen.getByRole("button", { name: "Continue" }));

    await waitFor(() => {
      expect(screen.getByText("Sending code...")).toBeInTheDocument();
    });
  });

  it("shows verification code step after sending code", async () => {
    mockSendCode.mockResolvedValueOnce(undefined);
    const user = userEvent.setup();
    render(<LoginPage />);

    await user.type(screen.getByLabelText("Email"), "test@multica.ai");
    await user.click(screen.getByRole("button", { name: "Continue" }));

    await waitFor(() => {
      expect(screen.getByText("Check your email")).toBeInTheDocument();
    });
  });

  it("shows error when sendCode fails", async () => {
    mockSendCode.mockRejectedValueOnce(new Error("Network error"));
    const user = userEvent.setup();
    render(<LoginPage />);

    await user.type(screen.getByLabelText("Email"), "test@multica.ai");
    await user.click(screen.getByRole("button", { name: "Continue" }));

    await waitFor(() => {
      expect(screen.getByText("Network error")).toBeInTheDocument();
    });
  });

  it("authorizes CLI from an existing cookie-backed session", async () => {
    mockSearchParams.set("cli_callback", "http://localhost:8765/callback");
    mockSearchParams.set("cli_state", "state-123");
    mockGetMe.mockResolvedValueOnce({ id: "user-1", email: "user@example.com" });
    mockGetSessionToken.mockResolvedValueOnce({ token: "session-jwt" });
    const user = userEvent.setup();

    render(<LoginPage />);

    await screen.findByText("Authorize CLI");
    await user.click(screen.getByRole("button", { name: "Authorize" }));

    await waitFor(() => {
      expect(mockGetSessionToken).toHaveBeenCalledTimes(1);
    });
    expect(window.location.href).toBe(
      "http://localhost:8765/callback?token=session-jwt&state=state-123",
    );
  });
});
