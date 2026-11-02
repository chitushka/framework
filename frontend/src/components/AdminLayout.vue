<script setup>
defineProps({ title: { type: String, required: true } })
</script>

<template>
  <div class="wrapper">
    <nav id="sidebar" class="sidebar js-sidebar">
      <div class="sidebar-content js-simplebar">
        <a class="sidebar-brand" href="/"><span class="align-middle">Framework</span></a>
        <ul class="sidebar-nav">
          <li class="sidebar-header">Управление</li>
          <li class="sidebar-item" :class="{ active: title === 'Панель управления' }">
            <a class="sidebar-link" href="/"><i class="align-middle" data-feather="sliders" /> <span>Дашборд</span></a>
          </li>
          <li class="sidebar-item" :class="{ active: title === 'Профиль' }">
            <a class="sidebar-link" href="/profile"><i class="align-middle" data-feather="user" /> <span>Профиль</span></a>
          </li>
        </ul>
      </div>
    </nav>
    <div class="main">
      <nav class="navbar navbar-expand navbar-light navbar-bg">
        <a class="sidebar-toggle js-sidebar-toggle"><i class="hamburger align-self-center" /></a>
        <div class="navbar-collapse collapse">
          <ul class="navbar-nav navbar-align">
            <li class="nav-item"><a class="nav-link" href="/profile">Администратор</a></li>
          </ul>
        </div>
      </nav>
      <main class="content">
        <div class="container-fluid p-0">
          <h1 class="h3 mb-3">{{ title }}</h1>
          <slot />
        </div>
      </main>
      <footer class="footer"><div class="container-fluid"><span class="text-muted">Framework · Vue 3 + AdminKit 3.5</span></div></footer>
    </div>
  </div>
</template>
