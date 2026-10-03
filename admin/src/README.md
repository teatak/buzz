# Buzz Admin Framework

The admin app is a Vite + React + Tailwind v4 + shadcn/ui surface.

## Structure

- `api/`: HTTP client and endpoint wrappers.
- `types/`: API and view model types.
- `router/`: hash route mapping.
- `lib/`: pure helpers.
- `components/ui/`: shadcn-style primitives. Add copied shadcn components here.
- `components/`: Buzz shared components built from `components/ui`.
- `layout/`: shell/sidebar/topbar components.
- `views/`: page-level views such as dashboard, providers, and models.

## Theme

Theme tokens in `styles.css` follow the `pudding-core` web UI style:

- neutral workspace and chrome
- indigo primary color
- 10px base radius
- dense admin/tooling layout

Use semantic tokens (`--background`, `--card`, `--primary`, `--border`) instead of hard-coded colors.

## Rules

- New reusable controls go in `components/ui` or `components`.
- New pages go in `views`.
- API response shapes go in `types/admin.ts`.
- Do not put new feature code in `main.tsx`; keep it as the app coordinator.
