import type { D1Migration } from "@cloudflare/vitest-plugin";

declare global {
  namespace Cloudflare {
    interface Env {
      TEST_MIGRATIONS: D1Migration[];
      TEST_APNS_PUBLIC_JWK: string;
      APNS_KEY_P8: string;
    }
  }
}
