import { defineComponent, h, ref, inject, onMounted, onUnmounted, nextTick } from 'vue'

const TYPE_META = {
  vscode: { icon: 'code-2', label: 'VS Code' },
  'ai-code': { icon: 'terminal', label: 'AI Code' },
}

const IDLE_TIMEOUT_MS = 60 * 60 * 1000

function formatCountdown(ms) {
  if (ms <= 0) return '0:00'
  const totalSec = Math.floor(ms / 1000)
  const m = Math.floor(totalSec / 60)
  const s = totalSec % 60
  return m + ':' + String(s).padStart(2, '0')
}

export default defineComponent({
  name: 'WorkspaceCard',
  props: {
    instance: { type: Object, required: true },
    pending: { type: String, default: null },
  },
  emits: ['start', 'stop', 'terminate'],
  setup(props, { emit }) {
    const { openPanel } = inject('iframePanel')
    const idleText = ref('')
    const idleWarn = ref(false)
    let timer = null

    function updateIdle() {
      const inst = props.instance
      if (inst.state !== 'running' || !inst.lastAccess) {
        idleText.value = ''
        return
      }
      const remaining = IDLE_TIMEOUT_MS - (Date.now() - inst.lastAccess)
      idleWarn.value = remaining < 10 * 60 * 1000
      idleText.value = 'Idle timeout: ' + formatCountdown(remaining)
    }

    onMounted(() => {
      updateIdle()
      timer = setInterval(updateIdle, 1000)
      nextTick(() => lucide.createIcons())
    })

    onUnmounted(() => {
      if (timer) clearInterval(timer)
    })

    return () => {
      const inst = props.instance
      const isRunning = inst.state === 'running'
      const type = inst.type || 'vscode'
      const meta = TYPE_META[type] || TYPE_META.vscode
      const badgeClass = isRunning ? 'running'
        : (inst.state === 'exited' || inst.state === 'dead') ? 'exited' : 'created'

      const headerChildren = [
        h('div', { class: `workspace-card__icon workspace-card__icon--${type}`,
          innerHTML: `<i data-lucide="${meta.icon}" style="width:20px;height:20px"></i>` }),
        h('div', { class: 'workspace-card__info' }, [
          h('div', { class: 'workspace-card__name' }, inst.name),
          h('div', { class: 'workspace-card__url' }, inst.url),
          h('div', { class: `workspace-card__type workspace-card__type--${type}` }, meta.label),
          inst.cluster && h('div', { class: 'cluster-meta', style: 'margin-top:4px',
            innerHTML: `<span class="cluster-meta__item"><i data-lucide="server" style="width:10px;height:10px;color:var(--accent-k8s)"></i> <span style="color:var(--accent-k8s)">${inst.cluster}</span></span>` }),
          idleText.value && h('div', {
            class: 'workspace-card__idle' + (idleWarn.value ? ' workspace-card__idle--warn' : ''),
            innerHTML: `<i data-lucide="clock" style="width:11px;height:11px"></i> <span class="idle-text">${idleText.value}</span>`,
          }),
        ]),
        h('div', { class: `workspace-card__badge workspace-card__badge--${badgeClass}` }, [
          h('span', { class: 'workspace-card__badge-dot' }),
          ` ${inst.state}`,
        ]),
      ]

      let actionChildren
      if (props.pending) {
        actionChildren = [
          h('button', { class: 'btn', disabled: true, innerHTML: `<span class="spinner"></span> ${props.pending}` }),
        ]
      } else {
        actionChildren = [
          h('button', {
            class: 'btn btn--primary',
            disabled: !isRunning,
            style: !isRunning ? 'opacity:0.4' : undefined,
            onClick: () => openPanel(inst.url, inst.name),
            innerHTML: '<i data-lucide="external-link" style="width:14px;height:14px"></i> Open',
          }),
          isRunning
            ? h('button', { class: 'btn', onClick: () => emit('stop'),
                innerHTML: '<i data-lucide="square" style="width:14px;height:14px"></i> Stop' })
            : h('button', { class: 'btn btn--primary', onClick: () => emit('start'),
                innerHTML: '<i data-lucide="play" style="width:14px;height:14px"></i> Start' }),
          h('button', { class: 'btn btn--danger', onClick: () => emit('terminate'),
            innerHTML: '<i data-lucide="trash-2" style="width:14px;height:14px"></i> Terminate' }),
        ]
      }

      return h('div', { class: 'workspace-card' }, [
        h('div', { class: 'workspace-card__header' }, headerChildren),
        h('div', { class: 'workspace-card__actions' }, actionChildren),
      ])
    }
  },
})
