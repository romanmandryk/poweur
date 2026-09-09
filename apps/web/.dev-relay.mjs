import { startRelay } from "./test/helpers/relay.mjs";
const relay = await startRelay();
console.log("RELAY_URL=" + relay.baseUrl);
process.on("SIGTERM", () => { relay.stop(); process.exit(0); });
setInterval(() => {}, 1 << 30);
