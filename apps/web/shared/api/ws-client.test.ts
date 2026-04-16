import { beforeEach, describe, expect, it, vi } from "vitest";
import { WSClient } from "./ws-client";

describe("WSClient", () => {
  const WebSocketMock = vi.fn();

  beforeEach(() => {
    WebSocketMock.mockReset();
    vi.stubGlobal("WebSocket", WebSocketMock);
  });

  it("connects without putting auth token in the WebSocket URL", () => {
    const client = new WSClient("ws://localhost:3000/ws");
    client.setAuth("secret-token", "ws-123");

    client.connect();

    expect(WebSocketMock).toHaveBeenCalledTimes(1);
    expect(WebSocketMock).toHaveBeenCalledWith("ws://localhost:3000/ws?workspace_id=ws-123");
  });
});
