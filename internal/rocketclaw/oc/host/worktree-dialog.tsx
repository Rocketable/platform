import type { Project, WorktreeDirectory } from "@opencode/client/promise"
import { useDialog } from "@opencode/ui/context/dialog"
import { getFilename, sameDirectory } from "@opencode/util/path"
import { createSignal, onMount } from "solid-js"
import { useLanguage } from "@/runtime/i18n/language"
import { PluginSelectDialog } from "@/runtime/plugin/dialogs"
import { useServer } from "@/runtime/server/current"
import { workspaceSelectionDirectories } from "@/workspaces/paths"

export function useWorktreeDialog() {
  const dialog = useDialog()
  const language = useLanguage()
  const server = useServer()

  return (options: {
    project: Project & { worktree: string; worktrees?: readonly WorktreeDirectory[] }
    omit: string
    onSelect: (value: string) => void
  }) => {
    const { project, omit, onSelect } = options

    void dialog.show(() => {
      const [listed, setListed] = createSignal<readonly WorktreeDirectory[]>([])
      onMount(async () => {
        await server.ctx.sync.worktrees.refresh(project.id)
        const items = await server.ctx.sync.worktrees.list(project.id)
        if (items) setListed(items)
      })

      return (
        <PluginSelectDialog
          options={{
            title: language.t("session.new.location.worktrees"),
            placeholder: language.t("session.new.workspace.search.placeholder"),
            options: [
              ...(sameDirectory(project.worktree, omit)
                ? []
                : [{ value: "main", title: project.name ?? getFilename(project.worktree), description: project.worktree }]),
              { value: "create", title: language.t("workspace.new") },
              ...workspaceSelectionDirectories(project, listed())
                .filter((directory) => !sameDirectory(directory, omit))
                .map((directory) => ({ value: directory, title: getFilename(directory), description: directory })),
            ],
          }}
          done={(value) => {
            if (value !== undefined) onSelect(value)
            dialog.close()
          }}
        />
      )
    })
  }
}
