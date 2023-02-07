import ctypes
parent = ctypes.cdll.LoadLibrary(None)

parent.TykGetData.argtypes = [ctypes.c_char_p]
parent.TykGetData.restype = ctypes.c_char_p

parent.TykStoreData.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_int]

class TykGateway():
    def log(self, level):
        message_p = ctypes.c_char_p(bytes(self, "utf-8"))
        level_p = ctypes.c_char_p(bytes(level, "utf-8"))
        parent.CoProcessLog(message_p, level_p)

    def log_error(self):
        message_p = ctypes.c_char_p(bytes(self, "utf-8"))
        level_p = ctypes.c_char_p(bytes("error", "utf-8"))
        parent.CoProcessLog(message_p, level_p)

    def get_data(self):
        key_p = ctypes.c_char_p(bytes(self, "utf-8"))
        return parent.TykGetData(key_p)

    def store_data(self, value, ttl):
        key_p = ctypes.c_char_p(bytes(self, "utf-8"))
        value_p = ctypes.c_char_p(bytes(value, "utf-8"))
        ttl_int = ctypes.c_int(ttl)
        parent.TykStoreData(key_p, value_p, ttl_int)

    def trigger_event(self, payload):
        name_p = ctypes.c_char_p(bytes(self, "utf-8"))
        payload_p = ctypes.c_char_p(bytes(payload, "utf-8"))
        parent.TykTriggerEvent(name_p, payload_p)