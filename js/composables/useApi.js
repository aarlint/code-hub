export function useApi(toast, wsConnected, fetchAll) {
  async function request(url, opts = {}) {
    const res = await fetch(url, opts)
    if (!res.ok) throw new Error(await res.text())
    return res
  }

  async function createInstance(type, cluster) {
    const body = { type }
    if (cluster) body.cluster = cluster
    const res = await request('/api/instances', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
    return res.json()
  }

  async function startInstance(id) {
    await request(`/api/instances/${id}/start`, { method: 'POST' })
  }

  async function stopInstance(id) {
    await request(`/api/instances/${id}/stop`, { method: 'POST' })
  }

  async function terminateInstance(id) {
    await request(`/api/instances/${id}`, { method: 'DELETE' })
  }

  async function createCluster(name, servers, agents, image) {
    const body = { name, servers, agents }
    if (image) body.image = image
    await request('/api/clusters', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
  }

  async function startCluster(name) {
    await request(`/api/clusters/${name}/start`, { method: 'POST' })
  }

  async function stopCluster(name) {
    await request(`/api/clusters/${name}/stop`, { method: 'POST' })
  }

  async function deleteCluster(name) {
    await request(`/api/clusters/${name}`, { method: 'DELETE' })
  }

  async function launchTerminal(name) {
    const res = await request(`/api/clusters/${name}/terminal`, { method: 'POST' })
    return res.json()
  }

  async function removeTerminal(name) {
    await request(`/api/clusters/${name}/terminal`, { method: 'DELETE' })
  }

  async function extendCluster(name) {
    await request(`/api/clusters/${name}/extend`, { method: 'POST' })
  }

  async function fetchInstances() {
    const res = await fetch('/api/instances')
    if (!res.ok) throw new Error('Failed to fetch')
    return res.json()
  }

  return {
    createInstance,
    startInstance,
    stopInstance,
    terminateInstance,
    createCluster,
    startCluster,
    stopCluster,
    deleteCluster,
    launchTerminal,
    removeTerminal,
    extendCluster,
    fetchInstances,
  }
}
