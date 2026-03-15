import { defineComponent, h, ref, inject, onMounted, onUnmounted, onUpdated, nextTick } from 'vue'

function formatCountdown(ms) {
  if (ms <= 0) return '0:00:00'
  const totalSec = Math.floor(ms / 1000)
  const hrs = Math.floor(totalSec / 3600)
  const mins = Math.floor((totalSec % 3600) / 60)
  const secs = totalSec % 60
  return hrs + ':' + String(mins).padStart(2, '0') + ':' + String(secs).padStart(2, '0')
}

export default defineComponent({
  name: 'ClusterCard',
  props: {
    cluster: { type: Object, required: true },
    pending: { type: String, default: null },
    idleTimeout: { type: Number, required: true },
  },
  emits: ['start', 'stop', 'delete', 'launch-terminal', 'remove-terminal', 'extend'],
  setup(props, { emit }) {
    const { openPanel } = inject('iframePanel')
    const idleText = ref('')
    const idleWarn = ref(false)
    let timer = null

    function updateIdle() {
      const cl = props.cluster
      if (cl.status !== 'running' || !cl.lastStart) {
        idleText.value = ''
        return
      }
      const remaining = props.idleTimeout - (Date.now() - cl.lastStart)
      idleWarn.value = remaining < 60 * 60 * 1000
      idleText.value = 'Auto-pause in ' + formatCountdown(remaining)
    }

    onMounted(() => {
      updateIdle()
      timer = setInterval(updateIdle, 1000)
      nextTick(() => lucide.createIcons())
    })

    onUnmounted(() => {
      if (timer) clearInterval(timer)
    })

    onUpdated(() => nextTick(() => lucide.createIcons()))

    return () => {
      const cl = props.cluster
      const isRunning = cl.status === 'running'
      const isPaused = cl.status === 'paused'
      const badgeClass = isRunning ? 'running' : isPaused ? 'paused' : cl.status === 'starting' ? 'starting' : 'stopped'
      const hasTerminal = cl.terminalState === 'running'

      // Services section
      const serviceRows = []
      if (isRunning || hasTerminal) {
        const dotClass = hasTerminal ? 'active' : 'inactive'
        const rowBtns = []

        if (isRunning) {
          rowBtns.push(`<button class="btn btn--terminal" data-action="launch-terminal" style="padding:5px 12px;font-size:0.6rem">
            <i data-lucide="terminal" style="width:12px;height:12px"></i> ${hasTerminal ? 'Restart' : 'Launch'}
          </button>`)
        }
        if (hasTerminal) {
          rowBtns.push(`<button class="btn btn--primary" data-action="open-panel" data-url="${cl.terminalUrl}" data-title="Terminal: ${cl.name}" style="padding:5px 12px;font-size:0.6rem">
            <i data-lucide="external-link" style="width:12px;height:12px"></i> Open
          </button>`)
          rowBtns.push(`<button class="btn btn--danger" data-action="remove-terminal" style="padding:5px 12px;font-size:0.6rem">
            <i data-lucide="x" style="width:12px;height:12px"></i> Remove
          </button>`)
        }

        serviceRows.push(h('div', { class: 'cluster-service-row', innerHTML:
          `<span class="cluster-service-row__label"><span class="cluster-service-row__dot cluster-service-row__dot--${dotClass}"></span> Terminal</span>${rowBtns.join('')}`,
          onClick: (e) => {
            const btn = e.target.closest('[data-action]')
            if (!btn) return
            const action = btn.dataset.action
            if (action === 'launch-terminal') emit('launch-terminal')
            if (action === 'remove-terminal') emit('remove-terminal')
            if (action === 'open-panel') openPanel(btn.dataset.url, btn.dataset.title)
          },
        }))
      }

      // Ingress rows
      if (cl.exposedApps && cl.exposedApps.length) {
        cl.exposedApps.forEach(app => {
          serviceRows.push(h('div', { class: 'cluster-service-row', innerHTML:
            `<span class="cluster-service-row__label"><span class="cluster-service-row__dot cluster-service-row__dot--active"></span> Ingress</span>
             <button class="btn btn--primary" data-action="open-panel" data-url="https://${app}.notdone.dev" data-title="${app}" style="padding:5px 12px;font-size:0.6rem">
               <i data-lucide="globe" style="width:12px;height:12px"></i> ${app}
             </button>`,
            onClick: (e) => {
              const btn = e.target.closest('[data-action="open-panel"]')
              if (btn) openPanel(btn.dataset.url, btn.dataset.title)
            },
          }))
        })
      }

      const infoChildren = [
        h('div', { class: 'workspace-card__name' }, cl.name),
        h('div', { class: 'workspace-card__type', style: 'color:var(--accent-k8s)' }, 'vCluster'),
      ]

      // Idle countdown for running clusters
      if (idleText.value) {
        infoChildren.push(h('div', {
          class: 'workspace-card__idle' + (idleWarn.value ? ' workspace-card__idle--warn' : ''),
          style: 'display:flex;align-items:center;gap:5px',
        }, [
          h('span', { innerHTML: `<i data-lucide="clock" style="width:11px;height:11px"></i>` }),
          h('span', { class: 'idle-text' }, idleText.value),
          h('button', {
            class: 'btn',
            style: 'padding:2px 8px;font-size:0.5rem;min-height:0;line-height:1',
            onClick: (e) => { e.stopPropagation(); emit('extend') },
            innerHTML: '<i data-lucide="timer-reset" style="width:10px;height:10px"></i> Extend',
          }),
        ]))
      }

      const children = [
        h('div', { class: 'workspace-card__header' }, [
          h('div', { class: 'workspace-card__icon workspace-card__icon--cluster',
            innerHTML: '<i data-lucide="server" style="width:20px;height:20px"></i>' }),
          h('div', { class: 'workspace-card__info' }, infoChildren),
          h('div', { class: `workspace-card__badge workspace-card__badge--${badgeClass}` }, [
            h('span', { class: 'workspace-card__badge-dot' }),
            ` ${cl.status}`,
          ]),
        ]),
      ]

      if (props.pending) {
        children.push(h('div', { class: 'workspace-card__actions' }, [
          h('button', { class: 'btn', disabled: true, innerHTML: `<span class="spinner"></span> ${props.pending}` }),
        ]))
      } else {
        if (serviceRows.length) {
          children.push(h('div', { class: 'cluster-services' }, serviceRows))
        }
        children.push(h('div', { class: 'workspace-card__actions' }, [
          (isRunning)
            ? h('button', { class: 'btn', onClick: () => emit('stop'),
                innerHTML: '<i data-lucide="pause" style="width:14px;height:14px"></i> Pause' })
            : h('button', { class: 'btn btn--primary', onClick: () => emit('start'),
                innerHTML: '<i data-lucide="play" style="width:14px;height:14px"></i> Resume' }),
          h('button', { class: 'btn btn--danger', onClick: () => emit('delete'),
            innerHTML: '<i data-lucide="trash-2" style="width:14px;height:14px"></i> Delete' }),
        ]))
      }

      return h('div', { class: 'workspace-card' }, children)
    }
  },
})
