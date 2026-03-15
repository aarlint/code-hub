import { defineComponent, h, ref, Teleport, onMounted, nextTick } from 'vue'

const TYPE_META = {
  vscode: { icon: 'code-2', label: 'VS Code' },
  'ai-code': { icon: 'terminal', label: 'AI Code' },
}

export default defineComponent({
  name: 'ClusterPickerDialog',
  props: {
    type: { type: String, required: true },
    clusters: { type: Array, required: true },
  },
  emits: ['create', 'cancel'],
  setup(props, { emit }) {
    const selected = ref('')

    onMounted(() => nextTick(() => lucide.createIcons()))

    function onOverlayClick(e) {
      if (e.target === e.currentTarget) emit('cancel')
    }

    const meta = TYPE_META[props.type] || TYPE_META.vscode

    return () => h(Teleport, { to: 'body' },
      h('div', { class: 'confirm-overlay', onClick: onOverlayClick }, [
        h('div', { class: 'confirm-dialog', style: 'text-align:left' }, [
          h('div', { class: 'confirm-dialog__title', style: 'text-align:center' }, `Create ${meta.label}`),

          h('div', { style: 'margin-bottom:20px' }, [
            h('label', { class: 'form-label' }, 'Connect to k3d cluster'),
            h('div', { style: 'display:flex;flex-direction:column;gap:6px' }, [
              // "No cluster" option
              h('label', {
                style: `display:flex;align-items:center;gap:8px;padding:10px 14px;background:var(--bg-deep);border:1px solid ${selected.value === '' ? 'var(--accent)' : 'var(--border)'};border-radius:8px;cursor:pointer;font-size:0.8rem;transition:border-color 0.2s`,
              }, [
                h('input', {
                  type: 'radio', name: 'cluster-pick', value: '',
                  checked: selected.value === '',
                  onChange: () => selected.value = '',
                  style: 'accent-color:var(--accent)',
                }),
                h('span', { style: 'color:var(--text-secondary)' }, 'No cluster'),
              ]),
              // Cluster options
              ...props.clusters.map(c =>
                h('label', {
                  style: `display:flex;align-items:center;gap:8px;padding:10px 14px;background:var(--bg-deep);border:1px solid ${selected.value === c.name ? 'var(--accent-k8s)' : 'var(--border)'};border-radius:8px;cursor:pointer;font-size:0.8rem;transition:border-color 0.2s`,
                }, [
                  h('input', {
                    type: 'radio', name: 'cluster-pick', value: c.name,
                    checked: selected.value === c.name,
                    onChange: () => selected.value = c.name,
                    style: 'accent-color:var(--accent-k8s)',
                  }),
                  h('i', { 'data-lucide': 'server', style: 'width:14px;height:14px;color:var(--accent-k8s)' }),
                  h('span', { style: 'color:var(--text-primary)' }, c.name),
                  h('span', {
                    style: "color:var(--text-dim);font-family:'JetBrains Mono',monospace;font-size:0.6rem;margin-left:auto",
                  }, `${c.nodes} node${c.nodes !== 1 ? 's' : ''}`),
                ])
              ),
            ]),
            h('div', { class: 'form-hint' }, 'The workspace will have kubectl access to the selected cluster.'),
          ]),

          h('div', { class: 'confirm-dialog__actions', style: 'justify-content:flex-end' }, [
            h('button', { class: 'btn', onClick: () => emit('cancel') }, 'Cancel'),
            h('button', {
              class: 'btn btn--primary',
              onClick: () => emit('create', selected.value),
              innerHTML: `<i data-lucide="${meta.icon}" style="width:14px;height:14px"></i> Create`,
            }),
          ]),
        ]),
      ])
    )
  },
})
