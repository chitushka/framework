<script setup>
defineProps({ title: { type: String, required: true } })
</script>

<template>
  <main class="d-flex w-100">
    <div class="container d-flex flex-column">
      <div class="row vh-100">
        <div class="col-sm-10 col-md-8 col-lg-6 col-xl-5 mx-auto d-table h-100">
          <div class="d-table-cell align-middle">
            <div class="text-center mt-4"><a class="h2 brand-mark text-decoration-none" href="/">Framework</a><h1 class="h3 mt-3">{{ title }}</h1></div>
            <slot />
          </div>
        </div>
      </div>
    </div>
  </main>
</template>
