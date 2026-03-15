import { defineComponent, h, ref, Teleport, onMounted, nextTick } from 'vue'

export default defineComponent({
  name: 'CreateClusterDialog',
  emits: ['create', 'cancel'],
  setup(props, { emit }) {
    const name = ref('')
    const servers = ref(1)
    const agents = ref(0)
    const image = ref('')
    const error = ref('')
    const creating = ref(false)

    const nameRe = /^[a-z][a-z0-9-]{0,19}$/

    onMounted(() => nextTick(() => {
      lucide.createIcons()
      document.getElementById('cluster-name-input')?.focus()
    }))

    function validate() {
      if (!nameRe.test(name.value.trim())) {
        error.value = 'Invalid name. Use lowercase letters, numbers, hyphens. Start with a letter.'
        return false
      }
      error.value = ''
      return true
    }

    function onSubmit() {
      if (!validate()) return
      creating.value = true
      emit('create', {
        name: name.value.trim(),
        servers: servers.value,
        agents: agents.value,
        image: image.value,
      })
    }

    function onKeydown(e) {
      if (e.key === 'Enter') onSubmit()
    }

    function onOverlayClick(e) {
      if (e.target === e.currentTarget) emit('cancel')
    }

    return () => h(Teleport, { to: 'body' },
      h('div', { class: 'confirm-overlay', onClick: onOverlayClick }, [
        h('div', { class: 'confirm-dialog', style: 'text-align:left;max-width:480px' }, [
          h('div', { class: 'confirm-dialog__title', style: 'text-align:center' }, 'Create Cluster'),

          h('div', { style: 'margin-bottom:16px' }, [
            h('label', { class: 'form-label', for: 'cluster-name-input' }, 'Cluster Name'),
            h('input', {
              class: 'form-input', id: 'cluster-name-input', type: 'text',
              placeholder: 'my-cluster', maxlength: 20, autocomplete: 'off', spellcheck: false,
              value: name.value,
              onInput: e => name.value = e.target.value,
              onKeydown,
            }),
            h('div', { class: 'form-hint' }, 'Lowercase letters, numbers, and hyphens. Must start with a letter.'),
            error.value && h('div', { class: 'form-error', style: 'display:block' }, error.value),
          ]),

          h('div', { style: 'display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-bottom:16px' }, [
            h('div', [
              h('label', { class: 'form-label', for: 'cluster-servers-input' }, 'Server Nodes'),
              h('input', {
                class: 'form-input', id: 'cluster-servers-input', type: 'number',
                value: servers.value, min: 1, max: 5, style: 'width:100%',
                onInput: e => servers.value = parseInt(e.target.value) || 1,
              }),
              h('div', { class: 'form-hint' }, 'Control plane nodes (1-5)'),
            ]),
            h('div', [
              h('label', { class: 'form-label', for: 'cluster-agents-input' }, 'Agent Nodes'),
              h('input', {
                class: 'form-input', id: 'cluster-agents-input', type: 'number',
                value: agents.value, min: 0, max: 10, style: 'width:100%',
                onInput: e => agents.value = parseInt(e.target.value) || 0,
              }),
              h('div', { class: 'form-hint' }, 'Worker nodes (0-10)'),
            ]),
          ]),

          h('div', { style: 'display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-bottom:16px' }, [
            h('div', [
              h('label', { class: 'form-label', for: 'cluster-version-input' }, 'K3s Version'),
              h('select', {
                class: 'form-input', id: 'cluster-version-input', style: 'width:100%',
                value: image.value,
                onChange: e => image.value = e.target.value,
              }, [
                h('option', { value: '' }, 'Latest'),
                h('option', { value: 'rancher/k3s:v1.31.4-k3s1' }, 'v1.31.4'),
                h('option', { value: 'rancher/k3s:v1.30.8-k3s1' }, 'v1.30.8'),
                h('option', { value: 'rancher/k3s:v1.29.12-k3s1' }, 'v1.29.12'),
                h('option', { value: 'rancher/k3s:v1.28.15-k3s1' }, 'v1.28.15'),
              ]),
            ]),
          ]),

          h('div', { class: 'confirm-dialog__actions', style: 'justify-content:flex-end' }, [
            h('button', { class: 'btn', onClick: () => emit('cancel') }, 'Cancel'),
            h('button', {
              class: 'btn btn--k8s',
              disabled: creating.value,
              onClick: onSubmit,
              innerHTML: creating.value
                ? '<span class="spinner"></span> Creating...'
                : '<i data-lucide="plus" style="width:14px;height:14px"></i> Create',
            }),
          ]),
        ]),
      ])
    )
  },
})
