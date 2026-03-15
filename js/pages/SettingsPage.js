import { defineComponent, h } from 'vue'

export default defineComponent({
  name: 'SettingsPage',
  setup() {
    function card(title, rows) {
      return h('div', { class: 'settings-card' }, [
        h('div', { class: 'settings-card__title' }, title),
        ...rows.map(([label, value]) =>
          h('div', { class: 'settings-card__row' }, [
            h('span', { class: 'settings-card__label' }, label),
            h('span', { class: 'settings-card__value' }, value),
          ])
        ),
      ])
    }

    return () => h('div', [
      h('div', { class: 'page-header' }, [
        h('h1', { class: 'page-header__title' }, ['Settings ', h('span', '// about')]),
        h('p', { class: 'page-header__sub' }, 'DevZone configuration and info'),
      ]),
      card('About', [
        ['Application', 'DevZone'],
        ['Description', 'Dev Environments in the Browser'],
        ['Routing', 'Traefik + Cloudflare'],
      ]),
      card('Workspace Types', [
        ['VS Code', 'linuxserver/code-server'],
        ['AI Code', 'Claude, Codex, Cursor'],
      ]),
      card('Infrastructure', [
        ['Domain', '*.notdone.dev'],
        ['Platform', 'Kubernetes (Pi k3s)'],
        ['Clusters', 'vCluster virtual clusters'],
        ['Data Persistence', 'PVCs (local-path)'],
        ['Auto-refresh', 'WebSocket (10s polling fallback)'],
      ]),
    ])
  },
})
