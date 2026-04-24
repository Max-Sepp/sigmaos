message(STATUS "Building ux proto library")

add_library(ux_proto STATIC
  ux.pb.cc
)

target_link_libraries(ux_proto PUBLIC rpc_proto)

target_include_directories(ux_proto PRIVATE
  ${CMAKE_SOURCE_DIR}
  ${Protobuf_INCLUDE_DIRS}
)
