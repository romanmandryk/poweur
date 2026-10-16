import type { PluginRuntime } from "openclaw/plugin-sdk/core";
import { createPluginRuntimeStore } from "openclaw/plugin-sdk/runtime-store";

const { setRuntime: setPoweurRuntime, getRuntime: getPoweurRuntime } = createPluginRuntimeStore<PluginRuntime>({
  pluginId: "poweur",
  errorMessage: "Poweur runtime not initialized",
});
export { getPoweurRuntime, setPoweurRuntime };
