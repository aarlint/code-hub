import { defineComponent, h, Teleport, inject, onUpdated, nextTick } from 'vue'

export default defineComponent({
  name: 'AppToast',
  setup() {
    const { toasts } = inject('toast')

    onUpdated(() => nextTick(() => lucide.createIcons()))

    return () => h(Teleport, { to: 'body' },
      h('div', { class: 'toast-container' },
        toasts.value.map(t => {
          const icon = t.type === 'success' ? 'check-circle' : 'alert-circle'
          const color = t.type === 'success' ? 'var(--accent-green)' : 'var(--accent-red)'
          return h('div', { class: `toast toast--${t.type}`, key: t.id, innerHTML:
            `<i data-lucide="${icon}" style="width:16px;height:16px;color:${color};flex-shrink:0"></i> ${t.msg}`,
          })
        })
      )
    )
  },
})
