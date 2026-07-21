import { writeFile } from 'node:fs/promises'

await writeFile(
  new URL('../../internal/webui/dist/.placeholder', import.meta.url),
  'Frontend production assets are generated here by npm run build.\n',
)
