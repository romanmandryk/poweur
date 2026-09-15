import { Moon, Sun } from "lucide-react";
import { toggleTheme, useUi } from "../state/ui";

export function ThemeToggle() {
  const theme = useUi((state) => state.theme);
  return (
    <button
      id="btn-theme"
      type="button"
      aria-label="Toggle theme"
      onClick={toggleTheme}
      className="btn-theme flex size-9 items-center justify-center rounded-full text-muted hover:bg-surface-2"
    >
      {theme === "dark" ? <Sun className="size-[18px]" /> : <Moon className="size-[18px]" />}
    </button>
  );
}
