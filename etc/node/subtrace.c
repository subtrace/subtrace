#include <errno.h>
#include <limits.h>
#include <fcntl.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/utsname.h>
#include <stdarg.h>
#include <stdio.h>

#include <node_api.h>

napi_value errorf(napi_env env, napi_status status, const char* fmt, ...) {
  const int size = 1024;
  char* buf = (char*) calloc(size, sizeof(char));
  if (buf == NULL) {
    napi_throw_error(env, NULL, "failed to calloc to deal with error");
    return NULL;
  }

  char* end = buf;

  end += snprintf(end, size-(end-buf), "subtrace: ");

  va_list args;
  va_start(args, fmt);
  end += vsnprintf(end, size-(end-buf), fmt, args);
  va_end(args);

  if (status != napi_ok) {
    if (status == napi_invalid_arg)                     end += snprintf(end, size-(end-buf), " (status=napi_invalid_arg)");
    if (status == napi_object_expected)                 end += snprintf(end, size-(end-buf), " (status=napi_object_expected)");
    if (status == napi_string_expected)                 end += snprintf(end, size-(end-buf), " (status=napi_string_expected)");
    if (status == napi_name_expected)                   end += snprintf(end, size-(end-buf), " (status=napi_name_expected)");
    if (status == napi_function_expected)               end += snprintf(end, size-(end-buf), " (status=napi_function_expected)");
    if (status == napi_number_expected)                 end += snprintf(end, size-(end-buf), " (status=napi_number_expected)");
    if (status == napi_boolean_expected)                end += snprintf(end, size-(end-buf), " (status=napi_boolean_expected)");
    if (status == napi_array_expected)                  end += snprintf(end, size-(end-buf), " (status=napi_array_expected)");
    if (status == napi_generic_failure)                 end += snprintf(end, size-(end-buf), " (status=napi_generic_failure)");
    if (status == napi_pending_exception)               end += snprintf(end, size-(end-buf), " (status=napi_pending_exception)");
    if (status == napi_cancelled)                       end += snprintf(end, size-(end-buf), " (status=napi_cancelled)");
    if (status == napi_escape_called_twice)             end += snprintf(end, size-(end-buf), " (status=napi_escape_called_twice)");
    if (status == napi_handle_scope_mismatch)           end += snprintf(end, size-(end-buf), " (status=napi_handle_scope_mismatch)");
    if (status == napi_callback_scope_mismatch)         end += snprintf(end, size-(end-buf), " (status=napi_callback_scope_mismatch)");
    if (status == napi_queue_full)                      end += snprintf(end, size-(end-buf), " (status=napi_queue_full)");
    if (status == napi_closing)                         end += snprintf(end, size-(end-buf), " (status=napi_closing)");
    if (status == napi_bigint_expected)                 end += snprintf(end, size-(end-buf), " (status=napi_bigint_expected)");
    if (status == napi_date_expected)                   end += snprintf(end, size-(end-buf), " (status=napi_date_expected)");
    if (status == napi_arraybuffer_expected)            end += snprintf(end, size-(end-buf), " (status=napi_arraybuffer_expected)");
    if (status == napi_detachable_arraybuffer_expected) end += snprintf(end, size-(end-buf), " (status=napi_detachable_arraybuffer_expected)");
    if (status == napi_would_deadlock)                  end += snprintf(end, size-(end-buf), " (status=napi_would_deadlock)");
    if (status == napi_no_external_buffers_allowed)     end += snprintf(end, size-(end-buf), " (status=napi_no_external_buffers_allowed)");
    if (status == napi_cannot_run_js)                   end += snprintf(end, size-(end-buf), " (status=napi_cannot_run_js)");
  }

  bool pending;
  if (napi_is_exception_pending(env, &pending) != napi_ok) {
    napi_throw_error(env, NULL, buf);
    goto end;
  }

  if (pending) {
    end += snprintf(end, size-(end-buf), "\n");
    fprintf(stderr, buf);
    goto end;
  }

  napi_throw_error(env, NULL, buf);
  goto end;

end:
  free(buf);
  return NULL;
}

napi_value init(napi_env env, napi_callback_info cbinfo) {
  napi_status status;

  struct utsname utsname;
  if (uname(&utsname) < 0) {
    return errorf(env, 0, "uname: errno=%d", errno);
  }

  char* kernel = NULL;
  if (strcmp(utsname.sysname, "Linux") == 0)  kernel = "linux";
  if (kernel == NULL) {
    return errorf(env, 0, "unsupported kernel: %s", utsname.sysname);
  }

  char* arch = NULL;
  if (strcmp(utsname.machine, "x86_64") == 0)  arch = "amd64";
  if (strcmp(utsname.machine, "x64") == 0)     arch = "amd64";
  if (strcmp(utsname.machine, "amd64") == 0)   arch = "amd64";
  if (strcmp(utsname.machine, "aarch64") == 0) arch = "arm64";
  if (strcmp(utsname.machine, "arm64") == 0)   arch = "arm64";
  if (arch == NULL) {
    return errorf(env, 0, "unsupported arch: %s", utsname.machine);
  }

  size_t count = 1;
  napi_value dirname;
  if ((status = napi_get_cb_info(env, cbinfo, &count, &dirname, NULL, NULL)) != napi_ok) {
    return errorf(env, status, "get dirname");
  }

  size_t used = 0;
  char pathname[PATH_MAX];
  memset(pathname, 0, PATH_MAX);
  if ((status = napi_get_value_string_utf8(env, dirname, pathname, PATH_MAX, &used)) != napi_ok) {
    return errorf(env, status, "read dirname as utf-8 string");
  }

  snprintf(pathname+used, PATH_MAX-used, "/subtrace-%s-%s", kernel, arch);

  napi_value global;
  if ((status = napi_get_global(env, &global)) != napi_ok) {
    return errorf(env, status, "failed to get global");
  }

  napi_value process;
  if ((status = napi_get_named_property(env, global, "process", &process)) != napi_ok) {
    return errorf(env, status, "failed to get global.process");
  }

  napi_value envv;
  if ((status = napi_get_named_property(env, process, "env", &envv)) != napi_ok) {
    return errorf(env, status, "failed to get process.env");
  }

  napi_value run_key;
  if ((status = napi_create_string_utf8(env, "SUBTRACE_RUN", NAPI_AUTO_LENGTH, &run_key)) != napi_ok) {
    return errorf(env, status, "create SUBTRACE_RUN string");
  }

  bool has_run;
  if ((status = napi_has_property(env, envv, run_key, &has_run)) != napi_ok) {
    return errorf(env, status, "failed to check if SUBTRACE_RUN present");
  }

  if (has_run) {
    return NULL;
  }

  napi_value argv;
  if ((status = napi_get_named_property(env, process, "argv", &argv)) != napi_ok) {
    return errorf(env, status, "failed to get process.argv");
  }

  uint32_t argc;
  if ((status = napi_get_array_length(env, argv, &argc)) != napi_ok) {
    return errorf(env, status, "failed to get process.argv.length");
  }

  char** argp = calloc(3+argc+1, sizeof(char*));
  if (argp == NULL) {
    return errorf(env, status, "failed to calloc args");
  }

  const char* prefix[3] = {"subtrace", "run", "--"};

  size_t total = 0;
  for (uint32_t i = 0; i < 3+argc; i++) {
    if (i < 3) {
      total += strlen(prefix[i]) + 1;
    } else {
      napi_value arg;
      if ((status = napi_get_element(env, argv, i-3, &arg)) != napi_ok) {
        free(argp);
        return errorf(env, status, "failed to get argv[%d]", i-3);
      }

      size_t len;
      if ((status = napi_get_value_string_utf8(env, arg, NULL, 0, &len)) != napi_ok) {
        free(argp);
        return errorf(env, status, "failed to get argv[%d].length", i-3);
      }

      total += len + 1;
    }
  }

  char* args = calloc(total, sizeof(char));

  used = 0;
  for (uint32_t i = 0; i < 3+argc; i++) {
    argp[i] = args+used;
    if (i < 3) {
      strncpy(argp[i], prefix[i], total-used);
      used += strlen(prefix[i]) + 1;
    } else {
      napi_value arg;
      if ((status = napi_get_element(env, argv, i-3, &arg)) != napi_ok) {
        free(argp);
        free(args);
        return errorf(env, status, "failed to get argv[%d]", i-3);
      }

      size_t copied;
      if ((status = napi_get_value_string_utf8(env, arg, argp[i], total-used, &copied)) != napi_ok) {
        free(argp);
        free(args);
        return errorf(env, status, "failed to get argv[%d].length", i-3);
      }

      used += copied + 1;
    }
  }

  if (used != total) {
    free(argp);
    free(args);
    return errorf(env, status, "used != total: %d != %d", used, total);
  }

  for (int fd = 0; fd <= 2; fd++) {
    int flags = fcntl(fd, F_GETFD);
    if (flags < 0) {
      free(argp);
      free(args);
      return errorf(env, status, "fcntl(%d, F_GETFD): errno=%d", fd, errno);
    }

    flags &= ~FD_CLOEXEC;

    if (fcntl(fd, F_SETFD, flags) < 0) {
      free(argp);
      free(args);
      return errorf(env, status, "fcntl(%d, F_SETFD): errno=%d", fd, errno);
    }
  }

  if (execv(pathname, argp) < 0) {
    free(argp);
    free(args);
    return errorf(env, status, "execv failed: errno=%d", errno);
  }

  return errorf(env, 0, "unreachable");
}

napi_value Init(napi_env env, napi_value exports) {
  napi_value fn;
  napi_create_function(env, NULL, 0, init, NULL, &fn);
  napi_set_named_property(env, exports, "init", fn);
  return exports;
}

NAPI_MODULE(NODE_GYP_MODULE_NAME, Init)
