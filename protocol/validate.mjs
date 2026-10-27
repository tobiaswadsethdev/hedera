// Validates fixtures/messages.json against schemas/hedera-v1.schema.json and
// checks that every binary fixture's frame_hex matches its decoded fields.
import { readFileSync } from "node:fs";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const read = (p) => JSON.parse(readFileSync(new URL(p, import.meta.url), "utf8"));
const schema = read("./schemas/hedera-v1.schema.json");
const messages = read("./fixtures/messages.json");
const binary = read("./fixtures/binary.json");

const ajv = new Ajv2020({ allErrors: true, strict: false });
addFormats(ajv);
ajv.addSchema(schema);
const get = (ref) => {
  const v = ajv.getSchema(`${schema.$id}#/$defs/${ref}`);
  if (!v) throw new Error(`missing schema ${ref}`);
  return v;
};

let failures = 0;
const fail = (name, why) => {
  failures++;
  console.error(`FAIL ${name}: ${why}`);
};

for (const f of messages) {
  const entry = f.direction === "client_to_agent" ? "request" : "agentMessage";
  const v = get(entry);
  if (!v(f.message)) fail(f.name, ajv.errorsText(v.errors));
  if (f.direction === "client_to_agent" && !schema.$defs.requests[f.message.t]) {
    fail(f.name, `request type ${f.message.t} has no schema`);
  }
  if (f.response_to) {
    const r = get(`results/${f.response_to}`);
    if (!r(f.message)) fail(f.name, ajv.errorsText(r.errors));
  }
}

for (const f of binary) {
  const buf = Buffer.from(f.frame_hex, "hex");
  const op = { 1: "output", 2: "input" }[buf[0]];
  if (op !== f.op) fail(f.name, `op ${op} != ${f.op}`);
  if (buf.readUInt32BE(1) !== f.channel) fail(f.name, "channel mismatch");
  let rest = buf.subarray(5);
  if (op === "output") {
    if (Number(buf.readBigUInt64BE(5)) !== f.offset) fail(f.name, "offset mismatch");
    rest = buf.subarray(13);
  }
  if (rest.toString("hex") !== f.payload_hex) fail(f.name, "payload mismatch");
}

if (failures) {
  console.error(`${failures} fixture(s) failed`);
  process.exit(1);
}
console.log(`ok: ${messages.length} message fixtures, ${binary.length} binary fixtures`);
