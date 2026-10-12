// Run: node scripts/check-api-logic.ts   (Node 22.18+; no test framework needed)
import assert from "node:assert/strict";
import { ApiError, humanizeApiError, parseErrorCode, SESSION_EXPIRED_MESSAGE } from "../src/lib/errors.ts";

let passed = 0;
function check(name: string, fn: () => void | Promise<void>): Promise<void> {
  return Promise.resolve(fn()).then(
    () => {
      passed++;
    },
    (e) => {
      console.error(`FAIL ${name}`);
      throw e;
    }
  );
}

// ---- errors.ts
await check("parseErrorCode reads {error}", () => {
  assert.equal(parseErrorCode('{"error":"invalid_request"}\n'), "invalid_request");
  assert.equal(parseErrorCode('{"error":"Project not found"}'), "Project not found");
  assert.equal(parseErrorCode(""), "");
  assert.equal(parseErrorCode("<html>502</html>"), "");
  assert.equal(parseErrorCode('{"error":42}'), "");
  assert.equal(parseErrorCode("null"), "");
});

await check("humanizeApiError maps every documented code", () => {
  const codes = [
    "invalid_request", "password_too_short", "password_too_long", "invalid_repo_url", "service_not_found",
    "invalid_env_var", "request_too_large", "too_many_requests", "too_many_streams", "invalid_ticket",
    "purge_choice_required", "invalid_purge_data",
  ];
  for (const c of codes) {
    const m = humanizeApiError(new ApiError(400, c));
    assert.ok(m.length > 10 && !m.includes(c), `${c} -> ${m}`);
  }
});

await check("humanizeApiError status fallbacks", () => {
  assert.equal(humanizeApiError(new ApiError(401, "")), SESSION_EXPIRED_MESSAGE);
  assert.match(humanizeApiError(new ApiError(404, "")), /not found/i);
  assert.match(humanizeApiError(new ApiError(429, "")), /too many/i);
  assert.match(humanizeApiError(new ApiError(503, "")), /unavailable/i);
  assert.match(humanizeApiError(new ApiError(500, "")), /went wrong/i);
  assert.match(humanizeApiError(new ApiError(502, "")), /went wrong/i);
  assert.match(humanizeApiError(new ApiError(0, "network_error")), /reach the server/i);
  assert.match(humanizeApiError(new TypeError("Failed to fetch")), /reach the server/i);
  assert.match(humanizeApiError("weird"), /went wrong/i);
});

await check("humanizeApiError tolerates sentences and never leaks raw JSON", () => {
  assert.equal(humanizeApiError(new ApiError(404, "Project not found")), "That project no longer exists.");
  assert.equal(humanizeApiError(new ApiError(409, "Connection already exists")), "Connection already exists");
  assert.equal(humanizeApiError(new ApiError(401, "invalid_credentials")), "Invalid email or password.");
  assert.ok(!humanizeApiError(new ApiError(418, "teapot_code")).includes("teapot_code"));
  assert.ok(!humanizeApiError(new ApiError(400, "__proto__")).includes("function"));
});

await check("humanizeApiError overrides win", () => {
  assert.equal(humanizeApiError(new ApiError(400, "invalid_request"), { invalid_request: "X" }), "X");
});

console.log(`ok: ${passed} checks passed`);
