import { describe, expect, test } from "bun:test";
import {
  AccountsUnavailable,
  accountsCall,
  accountIdentity,
  isCompletedSession,
  quickLoginView,
} from "./accounts-client.ts";

describe("accounts responses", () => {
  test("a 2FA flow token is not a finished session", () => {
    expect(isCompletedSession({ next: "2fa", token: "flow" })).toBe(false);
    expect(isCompletedSession({ next: "2fa_setup", token: "flow" })).toBe(false);
    expect(isCompletedSession({ token: "session" })).toBe(true);
  });

  test("reads the account email the website returns", () => {
    expect(accountIdentity({ account: { email: "Owner@Example.com", username: "ada" } })).toEqual({
      email: "owner@example.com",
      name: "ada",
    });
    expect(accountIdentity({ username: "ada" })).toBeNull();
  });

  test("normalizes quick login codes", () => {
    expect(
      quickLoginView({
        device_code: "device",
        user_code: "ABCD",
        verification_url: "https://red-xai.com/quick-login",
        interval: 5,
      }),
    ).toMatchObject({
      deviceCode: "device",
      userCode: "ABCD",
      verificationUrl: "https://red-xai.com/quick-login",
      interval: 5,
    });
  });

  test("a dead accounts host is unavailable rather than a local error", async () => {
    const fetchImpl: typeof fetch = () => Promise.reject(new Error("offline"));
    await expect(accountsCall("http://127.0.0.1:9/v1/accounts", "login", { body: { login: "a", password: "b" }, fetch: fetchImpl })).rejects.toBeInstanceOf(
      AccountsUnavailable,
    );
  });
});
