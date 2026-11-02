<script setup>
defineProps({ code: { type: Number, required: true } })
</script>

<template>
  <div class="text-center"><h1 class="display-1 fw-bold">{{ code }}</h1><p class="h2">{{ code === 404 ? 'Страница не найдена' : 'Внутренняя ошибка сервера' }}</p><p class="lead">{{ code === 404 ? 'Проверьте адрес или вернитесь на главную.' : 'Попробуйте повторить запрос позднее.' }}</p><a class="btn btn-primary" href="/">На главную</a></div>
</template>
