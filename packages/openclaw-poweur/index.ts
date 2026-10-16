import { defineChannelPluginEntry } from "openclaw/plugin-sdk/channel-core";
import { poweurPlugin } from "./src/channel.js";
import { setPoweurRuntime } from "./src/runtime.js";

export default defineChannelPluginEntry({
  id: "poweur",
  name: "Poweur",
  description: "Poweur ID channel: end-to-end encrypted messages between Poweur IDs and your agent",
  plugin: poweurPlugin,
  setRuntime: setPoweurRuntime,
});
