import { generateKeyPairSync } from "node:crypto";
import { cloudflareTest, readD1Migrations } from "@cloudflare/vitest-plugin";
import { defineConfig } from "vitest/config";

// A throwaway signing key standing in for the APNs .p8 key; tests verify
// provider tokens against its public half.
const { privateKey, publicKey } = generateKeyPairSync("ec", { namedCurve: "prime256v1" });

export default defineConfig({
  plugins: [
    cloudflareTest(async () => ({
      wrangler: { configPath: "./wrangler.jsonc" },
      miniflare: {
        bindings: {
          TEST_MIGRATIONS: await readD1Migrations("./migrations"),
          APNS_KEY_P8: privateKey.export({ type: "pkcs8", format: "pem" }).toString(),
          TEST_APNS_PUBLIC_JWK: JSON.stringify(publicKey.export({ format: "jwk" })),
          APNS_KEY_ID: "TESTKEY123",
          APNS_TEAM_ID: "TESTTEAM12",
          APNS_TOPIC: "dev.hedera.test",
          // Never pick up a developer's local override.
          APNS_ORIGIN_OVERRIDE: "",
        },
      },
    })),
  ],
  test: { setupFiles: ["./test/setup.ts"] },
});
